package main

import (
	"fmt"
	"math/bits"
	"os"
	"time"
)

// A CacheEntry wraps a PNG-encoded image to stored in a Cache. The screenshot
// URL is used as the cache key.
type CacheEntry struct {
	Image     []byte
	Signature string
	URL       string
	last      time.Time
}

// IsEmpty reports whether e is a zero value CacheEntry.
func (e *CacheEntry) IsEmpty() bool {
	return e.Image == nil && e.Signature == "" && e.URL == ""
}

// A Cache is an in-memory key-value store of recently accessed CacheEntry
// values. A new (zero value) Cache must be initialized before use (see Init).
// Caches are safe for concurrent use by multiple goroutines.
//
// An entry that hasn't been requested for 12 hours is deleted from the Cache.
type Cache struct {
	entries               map[string]CacheEntry
	fallbackImage         []byte
	readQuery             chan string
	readReply, writeQuery chan CacheEntry
	readAllQuery          chan struct{}
	readAllReply          chan []CacheEntry
	ttl                   time.Duration
}

// Init initializes an existing Cache value for use through the Read and Write
// methods.
func (c *Cache) Init(ttl time.Duration) {
	*c = Cache{
		entries:       make(map[string]CacheEntry),
		fallbackImage: encodeEmptyPNG(OGImageWidth, OGImageHeight),
		readQuery:     make(chan string),
		readReply:     make(chan CacheEntry),
		writeQuery:    make(chan CacheEntry),
		readAllQuery:  make(chan struct{}),
		readAllReply:  make(chan []CacheEntry),
		ttl:           ttl,
	}
	go c.initFallbackImage()
	go c.serve()
}

func (c *Cache) ReadAll() []CacheEntry {
	c.readAllQuery <- struct{}{}
	return <-c.readAllReply
}

// Read returns the CacheEntry value at the given URL in the cache. If no entry
// was found, a zero value entry is returned.
func (c *Cache) Read(url string) CacheEntry {
	c.readQuery <- url
	return <-c.readReply
}

// Write writes a CacheEntry to the cache, using entry.URL as the key.
func (c *Cache) Write(entry CacheEntry) {
	c.writeQuery <- entry
}

func (c *Cache) serve() {
	purge := time.NewTicker(5 * time.Minute)
	for {
		select {

		case <-c.readAllQuery:
			res := make([]CacheEntry, 0, len(c.entries))
			for _, entry := range c.entries {
				res = append(res, entry)
			}
			c.readAllReply <- res

		case url := <-c.readQuery:
			entry, exists := c.entries[url]
			replyEntry := entry
			if entry.URL != "" && entry.Image == nil {
				replyEntry.Image = c.fallbackImage
			}
			c.readReply <- replyEntry
			if exists {
				entry.last = time.Now()
				c.entries[url] = entry
			}

		case entry := <-c.writeQuery:
			entry.last = time.Now()
			c.entries[entry.URL] = entry

		case <-purge.C:
			size := 0
			for url, entry := range c.entries {
				elapsed := time.Since(entry.last)
				if elapsed > c.ttl {
					delete(c.entries, url)
					fmt.Fprintf(os.Stderr, "Clearing cache entry %s\n", url)
				} else {
					size += len(entry.Image)
				}
			}
			fmt.Fprintf(os.Stderr, "%s %d images in cache (%s)\n",
				time.Now().Format("[15:04:05]"), len(c.entries), fmtByteSize(size))
		}
	}
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
