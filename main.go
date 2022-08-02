package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

const (
	port           = 19165
	screenshotPath = "/api/spectura/v0/screenshot"
	infoPath       = "/api/spectura/v0/info"
)

var (
	decapURL          string
	maxImageSize      int
	signingKey        string
	signingSecret     string
	signingUniqueName string
	useSignatures     bool
	bgRateLimitTime   time.Duration
)

var cache Cache

func main() {
	rand.Seed(time.Now().UnixNano())

	cacheTTLString, _ := getenv("CACHE_TTL", "12h")
	cacheTTL, err := time.ParseDuration(cacheTTLString)
	if err != nil {
		log.Fatalf(`CACHE_TTL must be a valid duration such as "12h": %s\n`, err)
	}

	bgRateLimitTimeString, _ := getenv("BG_RATE_LIMIT_TIME", "3h")
	bgRateLimitTime, err = time.ParseDuration(bgRateLimitTimeString)
	if err != nil {
		log.Fatalf(`BG_RATE_LIMIT_TIME must be a valid duration such as "3h": %s\n`, err)
	}

	maxImageSizeString, _ := getenv("MAX_IMAGE_SIZE_MIB", "20")
	maxImageSizeMiB, err := strconv.Atoi(maxImageSizeString)
	if err != nil {
		log.Fatalf("MAX_IMAGE_SIZE_MIB must be a number: %s \n", err)
	}
	const bytesInMiB = 1 << 20
	maxImageSize = bytesInMiB * maxImageSizeMiB

	decapURL, err = getenv("DECAP_URL", "http://localhost:4531")
	if err != nil {
		log.Fatal(err)
	}
	useSignaturesString, _ := getenv("USE_SIGNATURES", "true")
	if useSignaturesString == "true" {
		useSignatures = true
		signingSecret, err = getenv("SIGNING_SECRET")
		if err != nil {
			log.Fatalf("%s (alternatively set USE_SIGNATURES=false)", err)
		}
		signingKey, err = getenv("SIGNING_KEY")
		if err != nil {
			log.Fatalf("%s (alternatively set USE_SIGNATURES=false)", err)
		}
		signingUniqueName, _ = getenv("SIGNING_UNIQUE_NAME", "jix_spectura")
	} else {
		useSignatures = false
	}

	cache.Init(cacheTTL)
	if err = loadImageConf(); err != nil {
		log.Fatalf(`Couldn't load image configuration from "%s": %s`, imageConfPath, err)
	}

	http.HandleFunc("/", http.NotFound)
	http.Handle(screenshotPath, http.HandlerFunc(screenshotHandler))
	http.Handle(infoPath, http.HandlerFunc(infoHandler))

	fmt.Fprintf(os.Stderr,
		"%s spectura is listening on http://localhost:%d%s\n",
		time.Now().Format("[15:04:05]"), port, screenshotPath,
	)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

func getenv(key string, fallback ...string) (string, error) {
	value := os.Getenv(key)
	if value != "" {
		return value, nil
	}
	if len(fallback) > 1 {
		return "", fmt.Errorf("getenv only takes 1 or 2 parameters")
	}
	if len(fallback) == 1 {
		return fallback[0], nil
	}
	return "", fmt.Errorf("missing environment variable %s", key)
}

// Check a JIX::UrlSignature hash signature
func checkSignature(url string, signature string) bool {
	h := hmac.New(sha1.New, []byte(signingKey))
	h.Write([]byte(signingUniqueName + ":" + url + signingSecret))
	signatureShouldBe := hex.EncodeToString(h.Sum(nil))
	return signature == signatureShouldBe
}

func screenshotHandler(w http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()
	rawURL := query.Get("url")
	if rawURL == "" {
		http.Error(w, `Query param "url" must be present`, http.StatusBadRequest)
		return
	}
	signature := query.Get("s")
	if useSignatures && signature == "" {
		http.Error(w, `Query param "s" must be present`, http.StatusBadRequest)
		return
	}

	targetURL, err := url.Parse(rawURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if useSignatures && !checkSignature(targetURL.String(), signature) {
		http.Error(w, "Signature check failed", http.StatusBadRequest)
		return
	}

	if query.Get("nocrop") != "" && !useSignatures {
		fmt.Fprintln(os.Stderr, "nocrop")

		entry := CacheEntry{URL: targetURL}
		err = entry.fetchAndCropImage(false, true)
		if err != nil {
			msg := fmt.Sprintf("nocrop fail: %s", err)
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(entry.Image)
		return
	}

	entry := cache.Read(targetURL.String())

	if query.Get("bg") != "" {
		if entry.IsEmpty() {
			entry.URL = targetURL
			entry.Signature = signature
		} else {
			elapsed := time.Since(entry.LastUpdated)
			if elapsed < bgRateLimitTime {
				msg := fmt.Sprintf("%s since last background request", elapsed)
				http.Error(w, msg, http.StatusTooManyRequests)
				return
			}
		}

		// Update timestamp in cache, so repeated queries are rejected
		// while the initial query is still being processed.
		entry.LastUpdated = time.Now()
		cache.Write(entry)

		cache.RefreshEntry(entry)
		return
	}

	if entry.IsEmpty() {
		entry.Signature = signature
		entry.URL = targetURL
		err = entry.fetchAndCropImage(false, false)
		switch {
		case err == nil:
			cache.Write(entry)
		case errors.Is(err, croppingError) || errors.Is(err, decapInternalError):
			cache.Write(entry)
			entry = cache.Read(entry.URL.String())
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cache.RefreshEntry(entry)
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(entry.Image)
}
