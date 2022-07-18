package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jobindex/spectura/decap"
)

const (
	fallbackImageURL = "https://www.jobindex.dk/img/jobindex20/spectura_adshare.png"
	imageConfPath    = "image_conf.json"
	OGImageHeight    = 314
	OGImageWidth     = 600
)

type SubImager interface {
	SubImage(r image.Rectangle) image.Image
}

func cropImage(m image.Image, voffset int) image.Image {
	sm := m.(SubImager)

	// If the image contains more than 30 background-looking rows, we remove
	// some of them by cropping a bit lower.
	const maxTopMargin = 25
	topMargin, color := countSingleColoredRows(m, voffset)
	origTopMargin, origVoffset := topMargin, voffset
	if topMargin > maxTopMargin {
		voffset += topMargin - maxTopMargin
		topMargin = maxTopMargin
	}
	logImgParam("vo", ", ", origVoffset, voffset)
	logImgParam("tm", ", ", origTopMargin, topMargin)
	origTopMargin, origVoffset = topMargin, voffset

	// We adjust cropping further by lowering the top margin to match any
	// existing right-left margins within the first maxTopMargin*2 rows of the
	// image.
	cropRect := image.Rect(0, voffset, OGImageWidth, voffset+(maxTopMargin*2))
	cropRect.Add(m.Bounds().Min)
	leftMargin, rightMargin := leftRightMargins(m, cropRect, color)

	var maxMargin int
	if rightMargin > leftMargin || rightMargin == 0 {
		maxMargin = leftMargin
	} else {
		maxMargin = rightMargin
	}
	if maxMargin > 0 && maxMargin < maxTopMargin {
		maxMargin += (maxTopMargin - maxMargin) / 2
	}
	fmt.Fprintf(os.Stderr, "mm: %d/%d -> %d, ", leftMargin, rightMargin, maxMargin)

	if maxMargin < topMargin {
		voffset += topMargin - maxMargin
	}
	logImgParam("vo", "\n", origVoffset, voffset)

	cropRect = image.Rect(0, voffset, OGImageWidth, voffset+OGImageHeight)
	cropRect.Add(m.Bounds().Min)
	return sm.SubImage(cropRect)
}

func countSingleColoredRows(m image.Image, offset int) (int, color.Color) {
	count := 0
	bounds := m.Bounds()
	minY := bounds.Min.Y + offset
	bgColor := m.At(bounds.Min.X, minY)

	r0, g0, b0, a0 := bgColor.RGBA()
	for y := minY; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r1, g1, b1, a1 := m.At(x, y).RGBA()
			if r0 == r1 && g0 == g1 && b0 == b1 && a0 == a1 {
				continue
			}
			return count, bgColor
		}
		count++
	}
	return count, bgColor
}

func leftRightMargins(m image.Image, r image.Rectangle, bgColor color.Color) (int, int) {
	bounds := m.Bounds().Intersect(r)
	minLeft, maxRight := bounds.Max.X-1, bounds.Min.X

	r0, g0, b0, a0 := bgColor.RGBA()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r1, g1, b1, a1 := m.At(x, y).RGBA()
			if r0 == r1 && g0 == g1 && b0 == b1 && a0 == a1 {
				continue
			}
			if x < minLeft {
				minLeft = x
			}
			break
		}
		for x := bounds.Max.X - 1; x >= bounds.Min.X; x-- {
			r1, g1, b1, a1 := m.At(x, y).RGBA()
			if r0 == r1 && g0 == g1 && b0 == b1 && a0 == a1 {
				continue
			}
			if x > maxRight {
				maxRight = x
			}
			break
		}
		if minLeft == bounds.Min.X && maxRight == bounds.Max.X-1 {
			break
		}
	}
	return minLeft, bounds.Max.X - maxRight - 1
}

func imageFromDecap(targetURL *url.URL, m *image.Image) error {

	extraDelay := getConfFromHostname(targetURL.Hostname()).Delay
	delay := 2500 + extraDelay

	fmt.Println(targetURL.String())
	logImgParam("d0", ", ", 2500, delay)
	logImgParam("d1", "\n", 1200)

	req := decap.Request{
		EmulateViewport: []string{strconv.Itoa(OGImageWidth), "1200", "mobile"},
		RenderDelay:     fmt.Sprintf("%dms", delay),
		Timeout:         "10s",
		Query: []*decap.QueryBlock{
			{
				Actions: []decap.Action{
					decapAction("navigate", targetURL.String()),
					decapAction("sleep"),
					decapAction("remove_info_boxes"),
					decapAction("remove_nav_sections"),
					decapAction("hide_nav_buttons"),
					decapAction("sleep", "1200ms"),
					decapAction("screenshot"),
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
	res, err = http.Post(fmt.Sprintf("%s/api/decap/v0/browse", decapURL), "application/json", &buf)
	if err != nil {
		return fmt.Errorf("couldn't connect to Decap: %s", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		msg, _ := io.ReadAll(res.Body)
		return fmt.Errorf(
			"unsuccesful Decap request: %s; %s", res.Status, msg)
	}

	if *m, err = png.Decode(res.Body); err != nil {
		return fmt.Errorf("couldn't decode PNG from Decap: %s", err)
	}
	return nil
}

func decapAction(list ...string) decap.Action {
	return decap.Action(list)
}

func (c *Cache) initFallbackImage() {
	_, err := url.ParseRequestURI(fallbackImageURL)
	if err != nil {
		if fallbackImageURL != "" {
			fmt.Fprintf(os.Stderr, "Bad fallback image URL: %s\n", err)
		}
		return
	}
	var res *http.Response
	for {
		var errMsg string
		res, err = http.Get(fallbackImageURL)
		switch {
		case err != nil:
			errMsg = err.Error()
		case res.StatusCode != 200:
			errMsg = res.Status
			res.Body.Close()
		default:
			var m image.Image
			if m, err = png.Decode(res.Body); err != nil {
				errMsg = err.Error()
				break
			}
			res.Body.Close()
			sm := m.(SubImager)
			m = sm.SubImage(image.Rect(0, 0, OGImageWidth, OGImageHeight))
			var buf bytes.Buffer
			if err = png.Encode(&buf, m); err != nil {
				errMsg = err.Error()
				break
			}
			fmt.Fprintf(os.Stderr, "Replacing fallback image with %s\n", fallbackImageURL)

			// lock cache while fallback image is replaced
			c.readQuery <- ""
			c.fallbackImage = buf.Bytes()
			<-c.readReply

			return
		}
		fmt.Fprintf(os.Stderr, "Bad fallback image (%s): %s\n", fallbackImageURL, errMsg)
		time.Sleep(15 * time.Second)
	}
}

func encodeEmptyPNG(width, height int) []byte {
	m := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		log.Fatal("Couldn't encode empty PNG")
	}
	return buf.Bytes()
}

type imageConfEntry struct {
	Delay   int `json:"delay"`
	Voffset int `json:"voffset"`
}

var globalImageConf map[string]imageConfEntry

func getConfFromHostname(hostname string) (entry imageConfEntry) {
	for sepCount := strings.Count(hostname, "."); sepCount > 0; sepCount-- {
		if hostnameEntry, ok := globalImageConf[hostname]; ok {
			if entry.Delay == 0 {
				entry.Delay = hostnameEntry.Delay
			}
			if entry.Voffset == 0 {
				entry.Voffset = hostnameEntry.Voffset
			}
			if entry.Delay != 0 && entry.Voffset != 0 {
				return entry
			}
		}
		hostname = strings.SplitN(hostname, ".", 2)[1]
	}
	return entry
}

func loadImageConf() error {
	globalImageConf = make(map[string]imageConfEntry)

	_, err := url.ParseRequestURI(imageConfPath)
	if err == nil {
		var res *http.Response
		res, err = http.Get(imageConfPath)
		if err == nil {
			err = json.NewDecoder(res.Body).Decode(&globalImageConf)
			res.Body.Close()
		}
	} else {
		var content []byte
		content, err = ioutil.ReadFile(imageConfPath)
		if err == nil {
			err = json.Unmarshal(content, &globalImageConf)
		}
	}
	return err
}

func logImgParam(name, sep string, param ...int) {
	switch len(param) {
	case 2:
		old, new := param[0], param[1]
		if old != new {
			fmt.Fprintf(os.Stderr, "%s: %d -> %d%s", name, old, new, sep)
			return
		}
		fallthrough
	case 1:
		par := param[0]
		fmt.Fprintf(os.Stderr, "%s: %d%s", name, par, sep)
	}
}
