package main

import (
	"html/template"
	"log"
	"net/http"
	"net/url"
)

type RenderableCacheEntry struct {
	SpecturaUrl string
	Size        string
	Url         string
}

type RenderableInfo struct {
	CacheEntries []RenderableCacheEntry
	TotalSize    string
	TotalEntries int
}

func infoHandler(w http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()
    var tmpl *template.Template
    if (query.Get("grid") == "") {
        tmpl = template.Must(template.ParseFiles("templates/info.tmpl"))
    } else {
        tmpl = template.Must(template.ParseFiles("templates/grid.tmpl"))
    }
	entries := cache.ReadAll()
	size := 0
	renderableEntries := []RenderableCacheEntry{}
	for _, entry := range entries {
		size += len(entry.Image)
		renderableEntries = append(renderableEntries,
			RenderableCacheEntry{
				entry.specturaUrl(),
				fmtByteSize(len(entry.Image)),
				entry.URL,
			})
	}

	err := tmpl.Execute(w, RenderableInfo{renderableEntries, fmtByteSize(size), len(entries)})
	if err != nil {
		panic(err)
	}
}

func (e *CacheEntry) specturaUrl() string {
	specturaUrl, err := url.Parse(screenshotPath)
	if err != nil {
		log.Fatal("screenshotPath is not a valid URL")
	}
	query := specturaUrl.Query()
	query.Set("url", e.URL)
	if useSignatures {
		query.Set("s", e.Signature)
	}
	specturaUrl.RawQuery = query.Encode()
	return specturaUrl.String()
}
