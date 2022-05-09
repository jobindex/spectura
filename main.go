package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"math/bits"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"spectura/decap"
)

const (
	ttl          = time.Hour * 12
	jobadPath    = "/api/jobad/v0.1/"
	maxEntrySize = 2 << 22
	port         = 19165
	version      = "v0.1"
)

var cache Cache

func main() {
	http.HandleFunc("/", http.NotFound)
	http.Handle(jobadPath, http.HandlerFunc(jobadHandler))
	cache.Init()
	go cache.Serve()

	fmt.Fprintf(os.Stderr,
		"%s spectura is listening on http://localhost:%d%s\n",
		time.Now().Format("[15:04:05]"), port, jobadPath,
	)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

type CacheEntry struct {
	bytes []byte
	id    int
	last  time.Time
}

func (e *CacheEntry) IsEmpty() bool {
	return e.bytes == nil && e.id == 0
}

type Cache struct {
	entries               map[int]CacheEntry
	readQuery             chan int
	readReply, writeQuery chan CacheEntry
}

func (c *Cache) Init() {
	*c = Cache{
		entries:    make(map[int]CacheEntry),
		readQuery:  make(chan int),
		readReply:  make(chan CacheEntry),
		writeQuery: make(chan CacheEntry),
	}
}

func (c *Cache) Read(id int) CacheEntry {
	c.readQuery <- id
	return <-c.readReply
}

func (c *Cache) Write(entry CacheEntry) {
	c.writeQuery <- entry
}

func (c *Cache) Serve() {
	purge := time.NewTicker(5 * time.Minute)
	for {
		select {

		case id := <-c.readQuery:
			entry, exists := c.entries[id]
			c.readReply <- entry
			if exists {
				entry.last = time.Now()
				c.entries[id] = entry
			}

		case entry := <-c.writeQuery:
			entry.last = time.Now()
			c.entries[entry.id] = entry

		case <-purge.C:
			size := 0
			for id, entry := range c.entries {
				elapsed := time.Since(entry.last)
				if elapsed > ttl {
					delete(c.entries, id)
					fmt.Fprintf(os.Stderr, "Clearing cache entry %d\n", id)
				} else {
					size += len(entry.bytes)
				}
			}
			fmt.Fprintf(os.Stderr, "%s %d images in cache (%s)\n",
				time.Now().Format("[15:04:05]"), len(c.entries), fmtByteSize(size))
		}
	}
}

func jobadHandler(w http.ResponseWriter, req *http.Request) {
	segments := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	if len(segments) != 4 {
		msg := fmt.Sprintf("path must contain a single job ad ID: %s<id>/", jobadPath)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	id, err := strconv.Atoi(segments[3])
	if err != nil {
		msg := fmt.Sprintf(`non-numerical job ad ID: "%s"`, segments[3])
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	entry := cache.Read(id)
	if entry.IsEmpty() {
		var m image.Image
		if err = imageFromDecap(id, &m); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sm := m.(SubImager)
		m = sm.SubImage(image.Rect(0, 0, 600, 314))
		var buf bytes.Buffer
		if err = png.Encode(&buf, m); err != nil {
			msg := fmt.Sprintf("failed to encode the generated PNG: %s", err)
			http.Error(w, msg, http.StatusInternalServerError)
		}
		entry = CacheEntry{id: id, bytes: buf.Bytes()}
		if len(entry.bytes) > maxEntrySize {
			fmt.Fprintf(os.Stderr, "Warning: Caching object (%s) larger than %s\n",
				fmtByteSize(len(entry.bytes)), fmtByteSize(maxEntrySize))
		}
		cache.Write(entry)
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(entry.bytes)
}

func imageFromDecap(id int, m *image.Image) error {

	url := fmt.Sprintf("https://www.jobindex.dk/jobannonce/%d/?pictura=1", id)
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
					decapAction("screenshot", "element", "article"),
				},
			},
		},
	}

	var buf bytes.Buffer
	err := json.NewEncoder(&buf).Encode(req)
	if err != nil {
		return fmt.Errorf("couldn't encode JSON response body: %s", err)
	}

	var res *http.Response
	res, err = http.Post("http://localhost:4531/api/browse/v0.8/", "application/json", &buf)
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

func fmtByteSize(n int) string {
	switch exp := bits.Len(uint(n)) / 10; exp {
	case 0:
		return fmt.Sprintf("%d B", n)
	case 1, 2, 3, 4:
		return fmt.Sprintf("%d %sB", n>>(exp*10), "KMGT"[exp-1:exp])
	default:
		return fmt.Sprintf("%d TB", n>>40)
	}
}
