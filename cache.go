package main

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/jobindex/spectura/xlib"
)

// A CacheEntry wraps a PNG-encoded image to stored in a Cache. The screenshot
// URL is used as the cache key.
type CacheEntry struct {
	Expire       time.Time
	Image        []byte
	Signature    string
	URL          *url.URL
	EntryCreated time.Time
	ImageCreated time.Time
	LastFetched  time.Time
	Provenance   Provenance
	Score        int
}

// IsEmpty reports whether e is a zero value CacheEntry.
func (e *CacheEntry) IsEmpty() bool {
	return e.Image == nil && e.Signature == "" && e.URL == nil
}

func (e *CacheEntry) IsFailedImage() bool {
	return e.URL != nil && e.Image == nil
}

// merge takes an "old" and a "new" CacheEntry, and creates a copy of the old
// entry where some fields may have been overwritten by values from the newer
// entry. It uses the following rules when merging:
//
// Expire and URL are always kept as is.
//
// If the new Image is non-nil and has a higher Score than the old, both Image
// and Score are overwritten, and ImageCreated is set to the time of the merge.
// Otherwise old's Image and Score are kept.
//
// If EntryCreated, Provenance or Signature were empty, they are taken from new,
// otherwise the old values are used.
//
// The newest value of LastFetched is used.
func merge(old, new CacheEntry) CacheEntry {
	if new.Image != nil && new.Score > old.Score {
		old.Image = new.Image
		old.ImageCreated = time.Now()
		old.Score = new.Score
	}
	if old.Provenance.when.IsZero() {
		old.Provenance = new.Provenance
	}
	if old.Signature == "" {
		old.Signature = new.Signature
	}
	if old.EntryCreated.IsZero() {
		old.EntryCreated = new.EntryCreated
	}
	if new.LastFetched.After(old.LastFetched) {
		old.LastFetched = new.LastFetched
	}
	return old
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

// WriteMetadata writes a CacheEntry's metadata to the cache, using entry.URL as
// the key.
func (c *Cache) WriteMetadata(entry CacheEntry) {
	entry.Image = nil
	c.writeQuery <- entry
}

func (c *Cache) serve() {
	gcInterval := time.NewTicker(5 * time.Minute)
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
				c.entries[url] = entry
			}

		case entry := <-c.writeQuery:
			if oldEntry, exists := c.entries[entry.URL.String()]; exists {
				entry = merge(oldEntry, entry)
			} else {
				now := time.Now()
				entry.EntryCreated = now
				if entry.Image != nil {
					entry.ImageCreated = now
				}
			}
			c.entries[entry.URL.String()] = entry

		// TODO: Auto-refresh cached images if time.Since(entry.ImageCreated)
		// 	     is larger than e.g. 6 hours.

		case <-gcInterval.C:
			size := 0
			for url, entry := range c.entries {

				if time.Since(entry.EntryCreated) > c.ttl {
					delete(c.entries, url)
					fmt.Fprintf(os.Stderr, "Clearing cache entry %s\n", url)
				} else {
					size += len(entry.Image)
				}

				if time.Since(entry.ImageCreated) > 3*time.Hour {
					c.RefreshEntry(entry)
				}
			}
			fmt.Fprintf(os.Stderr,
				"%s %d images in cache (%s)\n",
				time.Now().Format("[15:04:05]"),
				len(c.entries),
				xlib.FmtByteSize(size, 3),
			)
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

	fmt.Fprintf(os.Stderr, "Cache refresh (score %d): %s\n", e.Score, e.URL)
	if err := e.fetchAndCropImage(true, false); err != nil {
		fmt.Fprintf(os.Stderr, "Giving up on image refresh: %s\n", err)
		return
	}
	cache.Write(e)

	// TODO: Only write the new image to cache if it is more information dense
	//       than the previous image.
}
