// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tileTemplate is the Leaflet tile URL a builder writes into a map. Every brace
// is a placeholder the browser fills in per tile, so it addresses no image.
const tileTemplate = "https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"

const pngBody = "\x89PNG\r\n\x1a\nthis is the image"

// serve starts a server for the images in a test, points the fetcher at it, and
// counts what reaches it. The fetcher refuses a loopback address in production,
// which is the whole point of it, so a test that wants a real response has to
// hand it a client that dials.
//
// The count is what the matcher is judged on. Skipping a fetch and failing one
// produce the same empty result, so a test that reads only the result cannot
// tell a URL that was left alone from a URL that was fetched and rejected.
func serve(t *testing.T, h http.HandlerFunc) (*httptest.Server, *int) {
	t.Helper()
	got := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got++
		h(w, r)
	}))
	t.Cleanup(s.Close)

	previous := fetcher
	fetcher = s.Client()
	t.Cleanup(func() { fetcher = previous })
	return s, &got
}

func png(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, pngBody) }

func TestImagesLeavesATileTemplateInCodeAlone(t *testing.T) {
	s, requests := serve(t, png)
	code := fmt.Sprintf("L.tileLayer('%s/{z}/{x}/{y}.png', {maxZoom: 19}).addTo(map);", s.URL)

	imgs, text := images(code)

	if *requests != 0 {
		t.Fatalf("a line of code caused %d requests", *requests)
	}
	if len(imgs) != 0 {
		t.Fatalf("read %d images from a line of code, want 0: %v", len(imgs), imgs)
	}
	if text != code {
		t.Fatalf("code was rewritten\n got: %q\nwant: %q", text, code)
	}
}

func TestImagesLeavesATemplateInMarkupAlone(t *testing.T) {
	s, requests := serve(t, png)
	markup := fmt.Sprintf(`<img src="%s/{z}/{x}/{y}.png" alt="tile">`, s.URL)

	imgs, text := images(markup)

	if *requests != 0 {
		t.Fatalf("a template src caused %d requests", *requests)
	}
	if len(imgs) != 0 {
		t.Fatalf("read %d images from a template src, want 0: %v", len(imgs), imgs)
	}
	if text != markup {
		t.Fatalf("markup was rewritten\n got: %q\nwant: %q", text, markup)
	}
}

func TestImagesLeavesAURLInProseAlone(t *testing.T) {
	s, requests := serve(t, png)
	prose := fmt.Sprintf("the logo lives at %s/logo.png and is 2kb", s.URL)

	imgs, text := images(prose)

	if *requests != 0 {
		t.Fatalf("a URL in prose caused %d requests", *requests)
	}
	if len(imgs) != 0 {
		t.Fatalf("read %d images from prose, want 0: %v", len(imgs), imgs)
	}
	if text != prose {
		t.Fatalf("prose was rewritten\n got: %q\nwant: %q", text, prose)
	}
}

// A prompt full of code is the case the builder produces, and a markdown image
// is the shape most likely to be mistaken for a real one.
func TestImagesLeavesCodeAndMarkdownAlone(t *testing.T) {
	s, requests := serve(t, png)
	code := fmt.Sprintf("const hero = '%s/hero.jpg';\nbackground: url(%s/bg.webp);\n![alt](%s/fig.gif)",
		s.URL, s.URL, s.URL)

	imgs, text := images(code)

	if *requests != 0 {
		t.Fatalf("code caused %d requests", *requests)
	}
	if len(imgs) != 0 {
		t.Fatalf("read %d images from code, want 0: %v", len(imgs), imgs)
	}
	if text != code {
		t.Fatalf("code was rewritten\n got: %q\nwant: %q", text, code)
	}
}

func TestImagesReadsAnImageTag(t *testing.T) {
	s, _ := serve(t, png)
	message := fmt.Sprintf(`what is this? <img src="%s/cat.png">`, s.URL)

	imgs, text := images(message)

	if len(imgs) != 1 {
		t.Fatalf("read %d images, want 1: %v", len(imgs), imgs)
	}
	prefix := "data:image/png;base64,"
	if !strings.HasPrefix(imgs[0], prefix) {
		t.Fatalf("image is not a png data url: %q", imgs[0])
	}
	body, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(imgs[0], prefix))
	if err != nil {
		t.Fatalf("data url does not decode: %s", err)
	}
	if string(body) != pngBody {
		t.Fatalf("image body is %q, want %q", body, pngBody)
	}
	if text != "what is this? " {
		t.Fatalf("text after the tag was consumed is %q", text)
	}
}

func TestImagesKeepsAQueryOutOfTheMediaType(t *testing.T) {
	s, _ := serve(t, png)

	imgs, _ := images(fmt.Sprintf(`<img src="%s/cat.png?w=100">`, s.URL))

	if len(imgs) != 1 {
		t.Fatalf("read %d images, want 1", len(imgs))
	}
	if !strings.HasPrefix(imgs[0], "data:image/png;base64,") {
		t.Fatalf("media type carries the query: %q", imgs[0][:40])
	}
}

func TestImagesSkipsAnImageThatIsNotThere(t *testing.T) {
	s, _ := serve(t, http.NotFound)
	message := fmt.Sprintf(`read this <img src="%s/photo.png"> please`, s.URL)

	imgs, text := images(message)

	if len(imgs) != 0 {
		t.Fatalf("a 404 produced %d images, want 0: %v", len(imgs), imgs)
	}
	if text != message {
		t.Fatalf("an image that was not fetched had its tag removed anyway: %q", text)
	}
}

func TestImagesSkipsAnImageThatIsTooLarge(t *testing.T) {
	s, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxImageBytes+1))
	})

	imgs, _ := images(fmt.Sprintf(`<img src="%s/huge.png">`, s.URL))

	if len(imgs) != 0 {
		t.Fatalf("an oversize body produced %d images, want 0", len(imgs))
	}
}

func TestImagesStopsAtTheCap(t *testing.T) {
	s, _ := serve(t, png)

	var b strings.Builder
	for i := 0; i < maxImages*2; i++ {
		fmt.Fprintf(&b, `<img src="%s/cat%d.png">`, s.URL, i)
	}

	imgs, _ := images(b.String())

	if len(imgs) != maxImages {
		t.Fatalf("read %d images from %d tags, want the cap of %d", len(imgs), maxImages*2, maxImages)
	}
}

func TestImagesSkipsAHostThatDoesNotResolve(t *testing.T) {
	// .invalid is reserved and never resolves, so this exercises the fetcher
	// this service really ships with rather than a test client.
	message := `<img src="https://nothing.invalid/photo.png">`

	imgs, text := images(message)

	if len(imgs) != 0 {
		t.Fatalf("an unresolvable host produced %d images, want 0", len(imgs))
	}
	if text != message {
		t.Fatalf("text was rewritten for an image that was never fetched: %q", text)
	}
}

// Map code reaches a vision model whole: the tile template is the text of the
// message, not an address, and building the message cannot fail over it.
func TestVisionMessagesKeepTileCodeAndDoNotFail(t *testing.T) {
	code := fmt.Sprintf("L.tileLayer('%s').addTo(map);", tileTemplate)

	messages := OpenaiRawMessagesToGptVisionMessages([]*RawMessage{{Author: "User", Text: code}})

	if len(messages) != 1 {
		t.Fatalf("built %d messages, want 1", len(messages))
	}
	if len(messages[0].MultiContent) != 1 {
		t.Fatalf("message carries %d parts, want 1 text part", len(messages[0].MultiContent))
	}
	if got := messages[0].MultiContent[0].Text; got != code {
		t.Fatalf("the model would read\n got: %q\nwant: %q", got, code)
	}
}

func TestVisionMessagesSurviveAnImageThatIsNotThere(t *testing.T) {
	s, _ := serve(t, http.NotFound)
	text := fmt.Sprintf(`describe <img src="%s/photo.png">`, s.URL)

	messages := OpenaiRawMessagesToGptVisionMessages([]*RawMessage{{Author: "User", Text: text}})

	if len(messages) != 1 {
		t.Fatalf("built %d messages, want 1", len(messages))
	}
	if len(messages[0].MultiContent) != 1 {
		t.Fatalf("message carries %d parts, want the text part alone", len(messages[0].MultiContent))
	}
	if got := messages[0].MultiContent[0].Text; got != text {
		t.Fatalf("text part is %q, want %q", got, text)
	}
}

func TestImageExtRefusesWhatIsNotAnImageAddress(t *testing.T) {
	for _, src := range []string{
		tileTemplate,
		"https://tile.example.com/${z}/{x}.png",
		"/relative/cat.png",
		"cat.png",
		"file:///etc/passwd.png",
		"ftp://example.com/cat.png",
		"https://example.com/report.pdf",
		"https://example.com/",
		"data:image/png;base64,AAAA",
	} {
		if _, ok := imageExt(src); ok {
			t.Errorf("%q was accepted as an image address", src)
		}
	}
}

func TestImageExtAcceptsAnImageAddress(t *testing.T) {
	for src, want := range map[string]string{
		"https://example.com/cat.png":          "png",
		"http://example.com/a/b/cat.JPEG":      "jpeg",
		"https://example.com/cat.gif?v=2":      "gif",
		"https://example.com/photo.webp#frag":  "webp",
		"https://example.com/deep/path/x.jpg":  "jpg",
		"  https://example.com/spaced.png    ": "png",
	} {
		got, ok := imageExt(src)
		if !ok {
			t.Errorf("%q was refused", src)
			continue
		}
		if got != want {
			t.Errorf("%q has extension %q, want %q", src, got, want)
		}
	}
}
