package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
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
)

const (
	fallbackImageURL = "https://www.jobindex.dk/img/jobindex20/spectura_adshare.png"
	imageConfPath    = "image_conf.json"
)

type SubImager interface {
	SubImage(r image.Rectangle) image.Image
}

func imageFromDecap(targetURL *url.URL, m *image.Image) error {

	extraDelay := getConfFromHostname(targetURL.Hostname()).Delay
	delay := 2000 + extraDelay
	fmt.Fprintf(os.Stderr, "delay (incl. global conf) was %d\n", delay)

	req := decap.Request{
		EmulateViewport: []string{"600", "1200", "mobile"},
		RenderDelay:     fmt.Sprintf("%dms", delay),
		Timeout:         "10s",
		Query: []*decap.QueryBlock{
			{
				Actions: []decap.Action{
					decapAction("navigate", targetURL.String()),
					decapAction("sleep"),
					decapAction("remove_info_boxes"),
					decapAction("hide_navigation"),
					decapAction("sleep", "1000ms"),
					decapAction("screenshot"),
				},
			},
		},
	}

	fmt.Println(targetURL.String())

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
		default:
			var m image.Image
			if m, err = png.Decode(res.Body); err != nil {
				errMsg = err.Error()
				break
			}
			sm := m.(SubImager)
			m = sm.SubImage(image.Rect(0, 0, 600, 314))
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
