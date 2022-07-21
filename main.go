package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
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
)

var cache Cache

func main() {
	cacheTTLString, _ := getenv("CACHE_TTL", "12h")
	cacheTTL, err := time.ParseDuration(cacheTTLString)
	if err != nil {
		log.Fatalf(`CACHE_TTL must be a valid duration such as "12h": %s\n`, err)
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
		var m image.Image
		if err := imageFromDecap(&m, targetURL, true); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintln(os.Stderr, "nocrop")
		var buf bytes.Buffer
		if err = png.Encode(&buf, m); err != nil {
			msg := fmt.Sprintf("failed to encode the generated PNG: %s", err)
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(buf.Bytes())
		return
	}

	entry := cache.Read(targetURL.String())
	if entry.IsEmpty() {
		entry.Signature = signature
		entry.URL = targetURL.String()

		var m image.Image
		err = imageFromDecap(&m, targetURL, true)
		switch {
		case err == nil:
			m = cropImage(m, targetURL)
			if m.Bounds().Dy() < OGImageHeight {
				cache.Write(entry)
				entry = cache.Read(entry.URL)
				break
			}

			var buf bytes.Buffer
			if err = png.Encode(&buf, m); err != nil {
				msg := fmt.Sprintf("failed to encode the generated PNG: %s", err)
				http.Error(w, msg, http.StatusInternalServerError)
				return
			}
			entry.Image = buf.Bytes()
			if len(entry.Image) > maxImageSize {
				fmt.Fprintf(os.Stderr, "Warning: Caching object (%s) larger than %s\n",
					fmtByteSize(len(entry.Image)), fmtByteSize(maxImageSize))
			}
			cache.Write(entry)

		case strings.Contains(err.Error(), "500 Internal Server Error"):
			cache.Write(entry)
			entry = cache.Read(entry.URL)

		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(entry.Image)
}
