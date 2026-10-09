// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package controllers

// quote.go — the most a call on the paid lane can cost, read from its request before
// it is sent (quoteOf). It is what the call's seat holds (lane.go).
//
// A PROMPT IS READ AS A TOKEN PER BYTE. A tokenizer reads at least one byte per token —
// a vendor's byte-level vocabulary falls back to one token per byte for text it has no
// longer token for — so no text, however it is spelled, is billed as more tokens than
// it has bytes. Every byte of the body counts: messages, tools and schemas, JSON
// escapes and inline data alike. On top of that, what a vendor adds that the body does
// not carry:
//
//   - its own framing (frameTokens), and a tool it defines itself — computer, text
//     editor, bash, memory — whose instructions it writes in (toolTokens);
//   - a picture: an inline one in an image part, read as an image (pictureOf), is
//     counted as the tokens a vendor bills for its size (tiles) in place of its bytes,
//     never priced below pictureNano; one behind a URL as the largest picture
//     (remoteSide);
//   - a document — a PDF anywhere, a remote document, a stored file — as a whole
//     context (wholeTokens, or the model's own when it declares a larger one);
//   - a prompt the request writes to the vendor's cache (cache_control) at the price of
//     an hour-long cache write.
//
// A completion is its ceiling (the handler's, reserveCompletionTokens) for each choice
// asked (n), and a predicted output's bytes at the completion price. Tokens are priced
// at the most of the model's list price and its cost, the platform's and the org's.
// Images and video are their unit prices, speech its characters, a transcription its
// recording's length.

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/gif"  // registers GIF for image.DecodeConfig
	_ "image/jpeg" // registers JPEG for image.DecodeConfig
	_ "image/png"  // registers PNG for image.DecodeConfig
	"strings"

	"github.com/zap-proto/zip"
	_ "golang.org/x/image/webp" // registers WebP for image.DecodeConfig
)

const (
	// frameTokens is what a vendor adds to every request beyond its body: the turn
	// markers and the system text that introduces tools.
	frameTokens = 1024
	// toolTokens is what a tool the vendor defines adds beyond its own few bytes: the
	// schema and the instructions the vendor writes in for it.
	toolTokens = 2000
	// pictureTokens is the least a picture is read as. A large one is read as tiles of
	// tileSide pixels at tileTokens each, the most a vendor bills for its size.
	pictureTokens = 6000
	tileSide      = 768
	tileTokens    = 258
	// pictureNano is the least a picture is priced at, whatever the model's rate: a
	// vendor that bills a picture in tokens at a multiple of its text rate bills one at
	// most this. $0.01.
	pictureNano = 10_000_000
	// pictureBytes is the largest inline picture read as a picture; a larger one is
	// read as its bytes.
	pictureBytes = 20 << 20
	// remoteSide is the side a picture behind a URL is read as: the largest any vendor
	// takes.
	remoteSide = 8000
	// wholeTokens is what a document, a stored file or a remote document may fill: a
	// whole context, for a model that declares none larger.
	wholeTokens = 1_000_000
	// maxChoices is the most completions one chat request is answered with (n).
	maxChoices = 128
	// audioBytesPerSecond reads a recording's length from its size at 16 kbit/s, a
	// lower rate than any speech codec sends, so no recording is read as shorter than
	// it is.
	audioBytesPerSecond = 2000
	// pdfMagic is how a PDF's base64 begins: "%PDF-".
	pdfMagic = "JVBERi0"
)

// quote is what a call on the paid lane may cost, in nano-dollars, for each of width
// providers: fixed, and per for each completion token. floor and ceiling bound the
// completion ceiling it may be sent with — the least its handler sends, and what it
// asked for — both 0 for a call with no completion.
type quote struct {
	fixed, per     int64
	width          int64
	floor, ceiling int
}

// at is the quote at a completion ceiling of tokens.
func (q quote) at(tokens int) int64 { return q.width * (q.fixed + q.per*int64(tokens)) }

// fit is the highest completion ceiling, from floor up to ceiling, at which the quote
// is no more than room; false when even floor is more.
func (q quote) fit(room int64) (int, bool) {
	if q.at(q.ceiling) <= room {
		return q.ceiling, true
	}
	if q.per <= 0 || q.at(q.floor) > room {
		return 0, false
	}
	n := int((room/q.width - q.fixed) / q.per)
	return min(max(n, q.floor), q.ceiling), true
}

// quoteOf is the quote for a call to model at c's path, paid by org.
func quoteOf(c *zip.Ctx, org, model string) quote {
	path := strings.ToLower(strings.TrimRight(c.Path(), "/"))
	body := c.Body()
	q := quote{width: 1}
	switch {
	case ChatPath(path):
		p := readParts(body)
		r := rateOf(model, org, p.cached)
		var h struct {
			N                   int             `json:"n"`
			MaxTokens           int             `json:"max_tokens"`
			MaxCompletionTokens int             `json:"max_completion_tokens"`
			MaxOutputTokens     int             `json:"max_output_tokens"`
			Prediction          json.RawMessage `json:"prediction"`
			Thinking            struct {
				Budget int `json:"budget_tokens"`
			} `json:"thinking"`
		}
		_ = json.Unmarshal(body, &h)
		q.fixed = p.nanos(r, windowOf(model)) + int64(len(h.Prediction))*r.out
		q.per = r.out * int64(min(max(h.N, 1), maxChoices))
		q.ceiling = reserveCompletionTokens(model, cmp.Or(h.MaxTokens, h.MaxCompletionTokens, h.MaxOutputTokens))
		q.floor = min(q.ceiling, max(reserveCompletionFloor, h.Thinking.Budget+1))
		if (&ApiController{Ctx: c}).wantsFast() {
			q.width = fastWidth
		}
	case path == "/v1/images/generations":
		var r struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(body, &r)
		q.fixed = imageCostCents(model, max(r.N, 1)) * nanoPerCent
	case path == "/v1/videos/generations":
		q.fixed = videoCostCents(model, 1) * nanoPerCent
	case path == "/v1/audio/speech":
		var r struct {
			Input string `json:"input"`
		}
		_ = json.Unmarshal(body, &r)
		q.fixed = ttsCostNano(model, len(r.Input))
	case path == "/v1/audio/transcriptions", path == "/v1/audio/translations", strings.HasPrefix(path, transcriptPath):
		q.fixed = sttCostNano(model, float64(len(body))/audioBytesPerSecond)
	default:
		q.fixed = readParts(body).nanos(rateOf(model, org, false), windowOf(model))
	}
	q.fixed = max(q.fixed, 1)
	return q
}

// rate is what one token may cost, in nano-dollars: in, read as input — or, for a
// request that writes the vendor's cache, written to it for an hour — and out, written
// as completion.
type rate struct{ in, out int64 }

// rateOf is model's rate at the most of its list price and its cost, the platform's
// and org's own.
func rateOf(model, org string, cached bool) rate {
	var in, out float64
	for _, p := range []modelPrice{getModelPrice(model), getModelPriceForOrg(model, org)} {
		pin := p.InputPerMillion
		if cached {
			pin = max(pin*cacheWriteHourMultiple, cacheWriteRate(p.InputPerMillion, p.CacheWritePerMillion))
		}
		in, out = max(in, pin), max(out, p.OutputPerMillion)
		if p.costed() {
			cin := p.CostInPerMillion
			if cached {
				cin *= cacheWriteHourMultiple
			}
			in, out = max(in, cin), max(out, p.CostOutPerMillion)
		}
	}
	return rate{in: nanoPerToken(in), out: nanoPerToken(out)}
}

// windowOf is the most tokens a document can fill for model: wholeTokens, or the
// window the model declares when it is larger.
func windowOf(model string) int {
	if globalModelConfig != nil {
		return max(wholeTokens, globalModelConfig.ContextWindow(model))
	}
	return wholeTokens
}

// parts is a request body read for what a vendor bills as input.
type parts struct {
	// text is every byte not read as a picture.
	text int
	// pictures is each picture's tokens.
	pictures []int
	// whole counts the parts that may fill a whole context: a PDF, a remote document, a
	// stored file.
	whole int
	// tools counts the tools the vendor defines.
	tools int
	// cached: the request writes the vendor's cache.
	cached bool
}

// Content part types a picture is sent in, and that a document is.
var (
	pictureTypes  = map[string]bool{"image": true, "image_url": true, "input_image": true}
	documentTypes = map[string]bool{"document": true, "input_file": true}
	// sourceTypes name how a part's content travels, not what it is: they leave the
	// part's own type in force for what they carry.
	sourceTypes = map[string]bool{"base64": true, "url": true, "file": true, "text": true, "content": true}
	// pictureKeys carry a picture inside a picture part.
	pictureKeys = map[string]bool{"url": true, "image_url": true, "data": true}
	// fetchKeys name a remote document wherever they appear.
	fetchKeys = map[string]bool{"file_url": true, "file_uri": true, "fileuri": true}
	// fileKeys name a file the vendor stores.
	fileKeys = map[string]bool{"file_id": true, "fileid": true}
)

// readParts reads body. A body that is not JSON is its bytes.
func readParts(body []byte) parts {
	p := parts{text: len(body)}
	var v any
	if json.Unmarshal(body, &v) == nil {
		p.walk(v, "", "")
	}
	return p
}

// nanos is what the parts may cost at r, with window the most a document can fill.
func (p parts) nanos(r rate, window int) int64 {
	n := int64(max(p.text, 0)+frameTokens+p.tools*toolTokens) * r.in
	n += int64(p.whole) * int64(window) * r.in
	for _, t := range p.pictures {
		n += max(int64(t)*r.in, pictureNano)
	}
	return n
}

// walk reads v, found under key inside a part of type kind.
func (p *parts) walk(v any, key, kind string) {
	switch x := v.(type) {
	case map[string]any:
		if t, ok := x["type"].(string); ok && !sourceTypes[t] {
			kind = t
		}
		for k, e := range x {
			if strings.EqualFold(k, "cache_control") {
				p.cached = true
			}
			p.walk(e, k, kind)
		}
	case []any:
		tools := strings.EqualFold(key, "tools")
		for _, e := range x {
			if m, ok := e.(map[string]any); ok && tools {
				if t, _ := m["type"].(string); t != "" && t != "function" && t != "custom" {
					p.tools++
				}
			}
			p.walk(e, key, kind)
		}
	case string:
		p.read(x, key, kind)
	}
}

// read reads one string s found under key inside a part of type kind.
func (p *parts) read(s, key, kind string) {
	folded := strings.ToLower(key)
	b64 := s
	if rest, ok := strings.CutPrefix(s, "data:"); ok {
		head, payload, _ := strings.Cut(rest, ",")
		if !strings.HasSuffix(strings.ToLower(head), ";base64") {
			return
		}
		b64 = payload
	}
	switch {
	case strings.HasPrefix(b64, pdfMagic):
		p.whole++
	case remote(s) && pictureTypes[kind] && pictureKeys[folded]:
		p.pictures = append(p.pictures, tiles(remoteSide, remoteSide))
	case remote(s) && (fetchKeys[folded] || (documentTypes[kind] && pictureKeys[folded])):
		p.whole++
	case fileKeys[folded] && pictureTypes[kind]:
		p.pictures = append(p.pictures, tiles(remoteSide, remoteSide))
	case fileKeys[folded]:
		p.whole++
	case pictureTypes[kind] && pictureKeys[key]:
		if t, ok := pictureOf(b64); ok {
			p.pictures = append(p.pictures, t)
			p.text -= len(s)
		}
	}
}

// remote reports whether s is a URL a vendor fetches.
func remote(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "gs://")
}

// pictureOf reads base64 b64 as a picture: its tokens, and whether it decodes as a PNG,
// JPEG, GIF or WebP no larger than pictureBytes.
func pictureOf(b64 string) (int, bool) {
	if len(b64) < 16 || base64.StdEncoding.DecodedLen(len(b64)) > pictureBytes {
		return 0, false
	}
	cfg, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64)))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, false
	}
	return tiles(cfg.Width, cfg.Height), true
}

// tiles is what a w×h picture is read as: pictureTokens, or its tiles when more, and
// never more than a whole context, which no picture outgrows.
func tiles(w, h int) int {
	across, down := int64(w+tileSide-1)/tileSide, int64(h+tileSide-1)/tileSide
	return int(min(max(pictureTokens, across*down*tileTokens), wholeTokens))
}
