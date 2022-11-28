package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jobindex/spectura/decap"
	"github.com/jobindex/spectura/xlib"
)

const (
	OGImageHeight     = 314
	OGImageWidth      = 600
	fallbackImageURL  = "https://www.jobindex.dk/img/jobindex20/spectura_adshare.png"
	fastFollowupDelay = 1250 * time.Millisecond
	fastInitDelay     = 2500 * time.Millisecond
	fastTimeout       = 10 * time.Second
	imageConfPath     = "image_conf.json"
	slowFollowupDelay = 5 * time.Second
	slowInitDelay     = 10 * time.Second
	slowTimeout       = 25 * time.Second
)

var (
	croppingError      = errors.New("crop failure")
	decapInternalError = errors.New("internal Decap error")
	decapRequestError  = errors.New("Decap error")
)

type SubImager interface {
	SubImage(r image.Rectangle) image.Image
}

func (entry *CacheEntry) fetchAndCropImage(background, nocrop bool) error {
	var im image.Image
	err := imageFromDecap(&im, entry.URL, !background)
	if err != nil {
		return err
	}

	var ok bool
	var m *image.NRGBA
	if im, ok = im.(*image.NRGBA); ok {
		m = im.(*image.NRGBA)
	} else {
		fmt.Fprintf(os.Stderr, "Unexpected image type %T\n", im)
		b := im.Bounds()
		m = image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(m, m.Bounds(), im, b.Min, draw.Src)
	}

	if !nocrop {
		m = cropImage(m, entry.URL)
		if m.Bounds().Dy() < OGImageHeight {
			return croppingError
		}
	}
	var buf bytes.Buffer
	if err = png.Encode(&buf, m); err != nil {
		return fmt.Errorf("failed to encode the generated PNG: %w", err)
	}
	entry.Image = buf.Bytes()
	if len(entry.Image) > maxImageSize {
		fmt.Fprintf(os.Stderr, "Warning: Size of generated image (%s) exceeds %s\n",
			xlib.FmtByteSize(len(entry.Image), 3), xlib.FmtByteSize(maxImageSize, 3))
	}
	return nil
}

func cropImage(m *image.NRGBA, targetURL *url.URL) *image.NRGBA {
	voffset := getConfFromHostname(targetURL.Hostname()).Voffset

	// If the image contains more than 25 background-looking rows, we remove
	// some of them by cropping a bit lower.
	const maxTopMargin = 25
	topMargin, color := countSingleColoredRows(m, voffset)
	origTopMargin, origVoffset := topMargin, voffset
	if topMargin > maxTopMargin {
		voffset += topMargin - maxTopMargin
		topMargin = maxTopMargin
	}

	sep := ", "
	diffColoredMargin, _ := countSingleColoredRows(m, origVoffset+origTopMargin)
	if diffColoredMargin > maxTopMargin {
		voffset += diffColoredMargin - maxTopMargin
		topMargin = maxTopMargin
		sep = " (~c), "
	}

	logImgParam("vo", sep, origVoffset, voffset)
	logImgParam("tm", sep, origTopMargin, topMargin)
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
	return m.SubImage(cropRect).(*image.NRGBA)
}

func countSingleColoredRows(m *image.NRGBA, offset int) (int, color.NRGBA) {
	count := 0
	b := m.Bounds()
	minY := b.Min.Y + offset
	bgColor := m.NRGBAAt(b.Min.X, minY)

	for y := minY; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if m.NRGBAAt(x, y) != bgColor {
				return count, bgColor
			}
		}
		count++
	}
	return count, bgColor
}

func leftRightMargins(m *image.NRGBA, r image.Rectangle, bgColor color.NRGBA) (int, int) {
	b := m.Bounds().Intersect(r)
	minLeft, maxRight := b.Max.X-1, b.Min.X

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if m.NRGBAAt(x, y) == bgColor {
				continue
			}
			if x < minLeft {
				minLeft = x
			}
			break
		}
		for x := b.Max.X - 1; x >= b.Min.X; x-- {
			if m.NRGBAAt(x, y) == bgColor {
				continue
			}
			if x > maxRight {
				maxRight = x
			}
			break
		}
		if minLeft == b.Min.X && maxRight == b.Max.X-1 {
			break
		}
	}
	return minLeft, b.Max.X - maxRight - 1
}

func imageFromDecap(m *image.Image, targetURL *url.URL, fast bool) error {
	var d0, d1, timeout time.Duration
	if fast {
		d0 = fastInitDelay
		d0 += getConfFromHostname(targetURL.Hostname()).DelayDuration()
		d1 = fastFollowupDelay
		timeout = fastTimeout
	} else {
		d0 = slowInitDelay
		d1 = slowFollowupDelay
		timeout = slowTimeout
	}

	fmt.Println(targetURL.String())
	logImgParam("d0", ", ", int(d0.Milliseconds()))
	logImgParam("d1", "\n", int(d1.Milliseconds()))

	req := decap.Request{
		EmulateViewport: &decap.ViewportBlock{
			Width:  OGImageWidth,
			Height: 1200,
			Mobile: true,
		},
		RenderDelay: d0.String(),
		Timeout:     timeout.String(),
		Query: []*decap.QueryBlock{
			{
				Actions: []decap.Action{
					decapAction("navigate", targetURL.String()),
					decapAction("sleep", d0.String()),
					decapAction("remove_info_boxes"),
					decapAction("remove_nav_sections"),
					decapAction("hide_nav_buttons"),
					decapAction("sleep", d1.String()),
					decapAction("screenshot"),
				},
			},
		},
	}

	var buf bytes.Buffer
	err := json.NewEncoder(&buf).Encode(req)
	if err != nil {
		return fmt.Errorf("couldn't encode JSON response body: %w", err)
	}

	var res *http.Response
	res, err = http.Post(fmt.Sprintf("%s/api/decap/v0/browse", decapURL), "application/json", &buf)
	if err != nil {
		return fmt.Errorf("couldn't connect to Decap: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		msg, _ := io.ReadAll(res.Body)
		if res.StatusCode == 500 {
			return fmt.Errorf("%w: %s; %s", decapInternalError, res.Status, msg)
		}
		return fmt.Errorf("%w: %s; %s", decapRequestError, res.Status, msg)
	}

	if *m, err = png.Decode(res.Body); err != nil {
		return fmt.Errorf("couldn't decode PNG from Decap: %w", err)
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

func (c imageConfEntry) DelayDuration() time.Duration {
	d, err := time.ParseDuration(fmt.Sprintf("%dms", c.Delay))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Bad millisecond configuration: %w", err)
		return 0
	}
	return d
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
