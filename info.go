package main

import (
	"fmt"
	"html/template"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
)

type RenderableInfo struct {
	CacheEntries []CacheEntry
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
	for _, entry := range entries {
		size += len(entry.Image)
	}

	err := tmpl.Execute(w, RenderableInfo{entries, fmtByteSize(size), len(entries)})
	if err != nil {
		errId := rand.Intn(int(math.Pow10(8)))

		// log internal err message
		internalMsg := fmt.Sprintf("Error %d: Failed to execute template: %s", errId, err)
		fmt.Fprintf(os.Stderr, internalMsg)

		// return external err message
		externalMsg := fmt.Sprintf("Error %d: Failed to execute template", errId)
		http.Error(w, externalMsg, http.StatusInternalServerError)
		return
	}
}

func (e *CacheEntry) FmtSize() string {
	return fmtByteSize(len(e.Image))
}

func (e *CacheEntry) SpecturaURL() string {
	specturaURL, _ := url.Parse(screenshotPath)
	query := specturaURL.Query()
	query.Set("url", e.URL.String())
	if useSignatures {
		query.Set("s", e.Signature)
	}
	specturaURL.RawQuery = query.Encode()
	return specturaURL.String()
}
