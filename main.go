package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jobindex/spectura/decap"
)

const (
	port           = 19165
	screenshotPath = "/api/spectura/v0/screenshot"
	infoPath       = "/api/spectura/v0/info"
)

var (
	maxImageSize      int
	decapURL          string
	signingSecret     string
	signingKey        string
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

	http.HandleFunc("/", http.NotFound)
	http.Handle(screenshotPath, http.HandlerFunc(screenshotHandler))
	http.Handle(infoPath, http.HandlerFunc(infoHandler))
	cache.Init(cacheTTL)

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
	targetURL := query.Get("url")
	if targetURL == "" {
		http.Error(w, `Query param "url" must be present`, http.StatusBadRequest)
		return
	}
	signature := query.Get("s")
	if useSignatures && signature == "" {
		http.Error(w, `Query param "s" must be present`, http.StatusBadRequest)
		return
	}

	_, err := url.Parse(targetURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if useSignatures && !checkSignature(targetURL, signature) {
		http.Error(w, "Signature check failed", http.StatusBadRequest)
		return
	}

	entry := cache.Read(targetURL)
	if entry.IsEmpty() {
		var m image.Image
		if err = imageFromDecap(targetURL, &m); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sm := m.(SubImager)
		m = sm.SubImage(image.Rect(0, 0, 600, 314))
		var buf bytes.Buffer
		if err = png.Encode(&buf, m); err != nil {
			msg := fmt.Sprintf("failed to encode the generated PNG: %s", err)
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		entry = CacheEntry{URL: targetURL, Image: buf.Bytes()}
		if len(entry.Image) > maxImageSize {
			fmt.Fprintf(os.Stderr, "Warning: Caching object (%s) larger than %s\n",
				fmtByteSize(len(entry.Image)), fmtByteSize(maxImageSize))
		}
		cache.Write(entry)
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(entry.Image)
}

func imageFromDecap(url string, m *image.Image) error {

	req := decap.Request{
		EmulateViewport: []string{"600", "800", "mobile"},
		RenderDelay:     "100ms",
		Timeout:         "10s",
		Query: []*decap.QueryBlock{
			{
				Actions: []decap.Action{
					decapAction("navigate", url),
					decapAction("listen"),
					decapAction("sleep"),
					decapAction("remove_cookie_info"),
					decapAction("screenshot"),
				},
			},
		},
	}

	fmt.Println(url)

	var buf bytes.Buffer
	err := json.NewEncoder(&buf).Encode(req)
	if err != nil {
		return fmt.Errorf("couldn't encode JSON response body: %s", err)
	}

	var res *http.Response
	res, err = http.Post(fmt.Sprintf("%s/api/browse/v0.8/", decapURL), "application/json", &buf)
	if err != nil {
		return fmt.Errorf("couldn't connect to Decap: %s", err)
	}
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		msg, _ := io.ReadAll(res.Body)
		return fmt.Errorf(
			"unsuccesful Decap request: %d %s; %s",
			res.StatusCode, res.Status, msg,
		)
	}

	if *m, err = png.Decode(res.Body); err != nil {
		return fmt.Errorf("couldn't decode PNG from Decap: %s", err)
	}
	return nil
}

type SubImager interface {
	SubImage(r image.Rectangle) image.Image
}

func decapAction(list ...string) decap.Action {
	return decap.Action(list)
}
