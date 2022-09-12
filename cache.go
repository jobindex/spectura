package main

import (
	"fmt"
	"math/bits"
	"net/url"
	"os"
	"time"
)

// A CacheEntry wraps a PNG-encoded image to stored in a Cache. The screenshot // URL is used as the cache key.
type CacheEntry struct {
	Expire      time.Time
	Image       []byte
	Signature   string
	URL         *url.URL
	First       time.Time
	LastUpdated time.Time
	LastFetched time.Time
}

// IsEmpty reports whether e is a zero value CacheEntry.
func (e *CacheEntry) IsEmpty() bool {
	return e.Image == nil && e.Signature == "" && e.URL == nil
}

func (e *CacheEntry) IsFailedImage() bool {
	return e.URL != nil && e.Image == nil
}

// merge updates the fields in CacheEntry e based on the fields in o.
//
// Image is always updated, and LastUpdated is set to the time of the merge.
//
// First and Signature are updated if they were previously empty.
//
// LastFetched is updated if it contains a newer timestamp.
//
// URL is never updated.
func (e *CacheEntry) merge(o CacheEntry) {
	e.Image = o.Image
	e.LastUpdated = time.Now()
	if e.Signature == "" {
		e.Signature = o.Signature
	}
	if e.First.IsZero() {
		e.First = o.First
	}
	if o.LastFetched.After(e.LastFetched) {
		e.LastFetched = o.LastFetched
	}
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
	refreshQueue          chan chan struct{}
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
		refreshQueue:  make(chan chan struct{}, 10),
		ttl:           ttl,
	}
	go c.initFallbackImage()
	go c.serve()
	go c.scheduleRefresh()
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
//
// If the entry already exists, the cached image is overwritten. The client is
// responsible for avoiding wasteful updates (i.e. overwriting an image with an
// identical one).
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
			if entry.IsFailedImage() {
				replyEntry.Image = c.fallbackImage
			}
			c.readReply <- replyEntry
			if exists {
				entry.LastFetched = time.Now()
				c.entries[url] = entry
			}

		case entry := <-c.writeQuery:
			if oldEntry, exists := c.entries[entry.URL.String()]; exists {
				oldEntry.merge(entry)
			} else {
				now := time.Now()
				entry.First = now
				entry.LastFetched = now
				entry.LastUpdated = now
			}
			c.entries[entry.URL.String()] = entry

		// TODO: Auto-refresh cached images if time.Since(entry.LastUpdated)
		// 	     is larger than e.g. 6 hours.

		case <-purge.C:
			size := 0
			for url, entry := range c.entries {
				elapsed := time.Since(entry.LastUpdated)
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

// RefreshEntry queues a background job to capture a fresh screenshot for the
// cache entry and saves it in the cache. The Decap request uses longer sleep
// intervals than the one used for synchronous Spectura requests, which
// typically produces better screenshots.
func (c *Cache) RefreshEntry(e CacheEntry) {
	go c.runRefreshTask(e)
}

func (c *Cache) scheduleRefresh() {
	for {
		<-c.refreshQueue <- struct{}{}
		time.Sleep(5 * time.Second)
	}
}

func (c *Cache) runRefreshTask(e CacheEntry) {
	schedule := make(chan struct{})
	c.refreshQueue <- schedule
	<-schedule
	if err := e.fetchAndCropImage(true, false); err != nil {
		fmt.Fprintf(os.Stderr, "Giving up on image refresh: %s", err)
		return
	}
	cache.Write(e)

	// TODO: Only write the new image to cache if it is more information dense
	//       than the previous image.
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
