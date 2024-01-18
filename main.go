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
	"strings"
	"time"
)

const (
	port           = 19165
	screenshotPath = "/api/spectura/v0/screenshot"
	infoPath       = "/api/spectura/v0/info"
)

var (
	autoRefreshAfter         time.Duration
	bgRateLimitTime          time.Duration
	cacheTTL                 time.Duration
	decapURL                 string
	adminToken               string
	ignoreBackgroundRequests bool
	maxImageSize             int
	refreshTaskDelay         time.Duration
	scheduleInterval         time.Duration
	signingKey               string
	signingSecret            string
	signingUniqueName        string
	useSignatures            bool
	webhookURL               string
	webhookAuthHeader        string
)

var cache Cache

func main() {
	rand.Seed(time.Now().UnixNano())

	cacheTTLString, _ := getenv("CACHE_TTL", "48h")
	var err error
	cacheTTL, err = time.ParseDuration(cacheTTLString)
	if err != nil {
		log.Fatalf(`CACHE_TTL must be a valid duration such as "12h": %s\n`, err)
	}

	scheduleIntervalString, _ := getenv("SCHEDULE_INTERVAL", "5m")
	scheduleInterval, err = time.ParseDuration(scheduleIntervalString)
	if err != nil {
		log.Fatalf(`SCHEDULE_INTERVAL must be a valid duration such as "12h": %s\n`, err)
	}

	autoRefreshAfterString, _ := getenv("AUTO_REFRESH_AFTER", "6h")
	autoRefreshAfter, err = time.ParseDuration(autoRefreshAfterString)
	if err != nil {
		log.Fatalf(`AUTO_REFRESH_AFTER must be a valid duration such as "12h": %s\n`, err)
	}

	refreshTaskDelayString, _ := getenv("REFRESH_TASK_DELAY", "5s")
	refreshTaskDelay, err = time.ParseDuration(refreshTaskDelayString)
	if err != nil {
		log.Fatalf(`REFRESH_TASK_DELAY must be a valid duration such as "12h": %s\n`, err)
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

	adminToken, _ = getenv("ADMIN_TOKEN", "")

	ignoreBackgroundRequestsString, _ := getenv("IGNORE_BACKGROUND_REQUESTS", "false")
	ignoreBackgroundRequests = ignoreBackgroundRequestsString == "true"

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

	webhookURL, _ = getenv("WEBHOOK_URL")
	webhookAuthHeader, _ = getenv("WEBHOOK_AUTHORIZATION_HEADER")

	cache.Init()
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
func checkSignature(url string, signature string, expire string) bool {
	h := hmac.New(sha1.New, []byte(signingKey))
	h.Write([]byte(signingUniqueName + ":" + url + expire + signingSecret))
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

	expireRaw := query.Get("expire")
	var expire int64
	// Conditions where expire is left as 0, are handled after signature check.
	if expireRaw != "" {
		var err error
		expire, err = strconv.ParseInt(expireRaw, 10, 64)
		if err != nil {
			http.Error(w, `Query param "expire" must be a number`, http.StatusBadRequest)
			return
		}
	}

	targetURL, err := url.Parse(rawURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if useSignatures && !checkSignature(targetURL.String(), signature, expireRaw) {
		http.Error(w, "Signature check failed", http.StatusBadRequest)
		return
	}

	if expire == 0 || time.Now().After(time.Unix(expire, 0)) {
		// Redirect to fallback image
		http.Redirect(w, req, fallbackImageURL, http.StatusFound)
		return
	}

	if query.Get("nocrop") != "" && !useSignatures {
		entry := CacheEntry{URL: targetURL}
		fmt.Fprintf(os.Stderr, "Cache-miss (nocrop): %s\n", entry.URL)
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
		if ignoreBackgroundRequests {
			w.Write([]byte("Ignored"))
			return
		}
		if entry.IsEmpty() {
			entry.Expire = time.Unix(expire, 0)
			entry.Signature = signature
			entry.URL = targetURL
		} else {
			admin := query.Get("token") != "" && query.Get("token") == adminToken
			elapsed := time.Since(entry.LastRefreshAttempt)
			if !admin && elapsed < bgRateLimitTime {
				msg := fmt.Sprintf("%s since last background request", elapsed)
				http.Error(w, msg, http.StatusTooManyRequests)
				return
			}
		}

		// Create cache entry / update timestamp, so repeated background queries
		// can be rejected while this query is queued.
		cache.WriteMetadata(entry)

		go cache.runRefreshTask(entry)
		fmt.Fprint(w, "Success, refresh is now in progress. This can take up to 30 seconds.\n")
		return
	}

	if entry.IsEmpty() {
		entry = CacheEntry{
			Expire:      time.Unix(expire, 0),
			LastFetched: time.Now(),
			Provenance:  newProvenance(req),
			Signature:   signature,
			URL:         targetURL,
		}
		fmt.Fprintf(os.Stderr, "Cache miss: %s\n", entry.URL)
		err = entry.fetchAndCropImage(false, false)
		switch {
		case err == nil:
			cache.Write(entry)
		case errors.Is(err, croppingError) || errors.Is(err, decapInternalError):
			cache.WriteMetadata(entry)
			entry = cache.Read(entry.URL.String())
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		go cache.runRefreshTask(entry)
	} else if !strings.Contains(req.Referer(), infoPath) {
		fmt.Fprintf(os.Stderr, "Cache hit: %s\n", entry.URL)
		if entry.Provenance.when.IsZero() {
			entry.Provenance = newProvenance(req)
		}
		entry.LastFetched = time.Now()
		cache.WriteMetadata(entry)
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(entry.Image)
}

type Provenance struct {
	addr      string
	referer   string
	userAgent string
	when      time.Time
}

func newProvenance(req *http.Request) Provenance {
	return Provenance{
		addr:      req.RemoteAddr,
		referer:   req.Referer(),
		userAgent: req.UserAgent(),
		when:      time.Now(),
	}
}

func (p Provenance) String() string {
	if p.when.IsZero() {
		return p.when.Format(time.UnixDate)
	}
	s := fmt.Sprintf(
		"%s | %s | %s | %s",
		p.when.Format(time.UnixDate),
		p.addr,
		p.referer,
		p.userAgent,
	)
	const max = 150
	if len(s) > max {
		return fmt.Sprintf("%*s...", max-3, s)
	}
	return s
}
