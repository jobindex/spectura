package main

import (
    "math"
    "math/rand"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
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

var infoTmpl = template.Must(template.ParseFiles("templates/info.tmpl.html"))
var gridTmpl = template.Must(template.ParseFiles("templates/grid.tmpl.html"))

func infoHandler(w http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()
	var tmpl *template.Template
	if query.Get("grid") == "" {
        tmpl = infoTmpl
	} else {
        tmpl = gridTmpl
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
        errId := rand.Intn(int(math.Pow10(8)));

        // log internal err message
        internalMsg := fmt.Sprintf("Error %d: Failed to execute template: %s", errId, err)
		fmt.Fprintf(os.Stderr, internalMsg)

        // return external err message
        externalMsg := fmt.Sprintf("Error %d: Failed to execute template", errId)
		http.Error(w, externalMsg, http.StatusBadRequest)
		return
	}
}

func (e *CacheEntry) specturaUrl() string {
	specturaUrl, _ := url.Parse(screenshotPath)
	query := specturaUrl.Query()
	query.Set("url", e.URL)
	if useSignatures {
		query.Set("s", e.Signature)
	}
	specturaUrl.RawQuery = query.Encode()
	return specturaUrl.String()
}
