package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"text/template"
)

const headerFmt = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link href="https://cdn.jsdelivr.net/npm/bootstrap@5.2.0-beta1/dist/css/bootstrap.min.css" rel="stylesheet" integrity="sha384-0evHe/X+R7YkIZDRvuzKMRqM+OrBnVFBL6DOitfPri4tjfHxaWutUpFmBp4vmVor" crossorigin="anonymous">
<title>Spectura info</title>
</head>
<body>
<div class="container">
<table class="table table-hover">
<thead>
<tr>
<td></td><td>%d screenshots, %s</td>
</tr>
</thead>
<tbody>
`

const footer = `</tbody>
</table>
</div>
</body>
</html>`

func infoHandler(w http.ResponseWriter, req *http.Request) {

	const rowFmt = `<tr>
<td style="padding: 10px;"><img class="img-thumbnail" src="%s?url=%s"></td>
<td><dl>
<dt>Size</dt><dd>%s</dd>
<dt>URL</dt><dd><a href="%s">%s</a></dd>
</dl></td>
</tr>
`
	entries := cache.ReadAll()
	size := 0
	for _, entry := range entries {
		size += len(entry.Image)
	}
	fmt.Fprintf(w, headerFmt, len(entries), fmtByteSize(size))
	for _, entry := range entries {
		fmt.Fprintf(w, rowFmt,
			screenshotPath,
			template.HTMLEscapeString(url.QueryEscape(entry.URL)),
			fmtByteSize(len(entry.Image)),
			template.HTMLEscapeString(entry.URL),
			template.HTMLEscapeString(shortenURL(entry.URL, 80)),
		)
	}
	io.WriteString(w, footer)
}

func shortenURL(url string, max int) string {
	if len(url) <= max {
		return url
	}
	if max < 3 {
		max = 3
	}
	return fmt.Sprintf("%.*s...", max-3, url)
}
