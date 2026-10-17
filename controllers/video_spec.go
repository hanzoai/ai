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

// video_spec.go — a video model as its vendor lists it, and what one clip of it
// can cost.
//
// OpenRouter lists its video models at GET /v1/videos/models: the durations,
// resolutions, aspect ratios, sizes and frame images each takes, whether it can
// write audio, and its price as named SKUs (pricing_skus) in the vendor's own
// names and units — cents or dollars a second, per resolution and with or
// without audio, dollars a video token, a price an input image, a minimum a
// generation. The sync keeps those entries beside the text models (listing.go),
// so a video model is one more SKU of the resale family.
//
// A request is held to what the model lists before anything is spent (admit),
// and reserved at the most its clip can cost (ceiling): the dearest rate that
// can apply to the resolution, audio and inputs asked for, for the seconds
// asked for. The call then bills what the vendor says it cost (usage.cost),
// times the markup, never more than it reserved.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hanzoai/decimal"
)

// videoSpec is what a video model takes and what it costs, as its vendor lists it.
type videoSpec struct {
	Durations   []int             `json:"durations,omitempty"`
	Resolutions []string          `json:"resolutions,omitempty"`
	Aspects     []string          `json:"aspect_ratios,omitempty"`
	Sizes       []string          `json:"sizes,omitempty"`
	Frames      []string          `json:"frame_images,omitempty"` // first_frame, last_frame
	Audio       *bool             `json:"audio,omitempty"`        // writes audio: true by default, false never, absent unstated
	SKUs        map[string]string `json:"-"`                      // the vendor's price SKUs as listed
	rates       map[string]decimal.Decimal
}

// videoWire is the part of a /v1/videos/models entry that is about video.
type videoWire struct {
	Durations   []int                      `json:"supported_durations"`
	Resolutions []string                   `json:"supported_resolutions"`
	Aspects     []string                   `json:"supported_aspect_ratios"`
	Sizes       []string                   `json:"supported_sizes"`
	Frames      []string                   `json:"supported_frame_images"`
	Audio       *bool                      `json:"generate_audio"`
	SKUs        map[string]json.RawMessage `json:"pricing_skus"`
}

// spec reads an entry's video part, or false for an entry that is not a model
// that makes a clip from a prompt: no price SKUs, or no durations — an editor or
// an upscaler takes a video in, which this API does not send.
func (w videoWire) spec() (*videoSpec, bool) {
	if len(w.SKUs) == 0 || len(w.Durations) == 0 {
		return nil, false
	}
	s := &videoSpec{
		Durations: slices.Clone(w.Durations), Resolutions: lower(w.Resolutions), Aspects: w.Aspects,
		Sizes: w.Sizes, Frames: lower(w.Frames), Audio: w.Audio,
		SKUs: map[string]string{}, rates: map[string]decimal.Decimal{},
	}
	slices.Sort(s.Durations)
	for k, raw := range w.SKUs {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			str = strings.TrimSpace(string(raw))
		}
		d, err := decimal.Parse(str)
		if err != nil || d.Sign() < 0 {
			continue
		}
		key := strings.ToLower(k)
		s.SKUs[key] = d.String()
		s.rates[key] = d
	}
	if len(s.rates) == 0 {
		return nil, false
	}
	return s, true
}

func lower(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, strings.ToLower(strings.TrimSpace(v)))
	}
	return out
}

// videoAsk is one clip as a caller asks for it, after defaults are applied.
type videoAsk struct {
	Seconds     int
	Resolution  string // "720p"
	Aspect      string // "16:9"
	Size        string // "1280x720", when the caller named one
	First, Last string // first and last frame images
	References  []string
	Audio       *bool
	Seed        *int64
}

// resolutions are the heights a resolution names.
var resolutions = map[string]int{"480p": 480, "720p": 720, "768p": 768, "1080p": 1080, "1k": 1080, "2k": 1440, "4k": 2160}

// admit holds an ask to what the model lists, filling what the caller left out:
// the shortest duration of at least four seconds, and 720p where the model takes
// it. It answers the sentence a caller reads when the ask is outside the list.
func (s *videoSpec) admit(a *videoAsk) error {
	if a.Seconds == 0 {
		a.Seconds = s.Durations[len(s.Durations)-1]
		for _, d := range s.Durations {
			if d >= 4 {
				a.Seconds = d
				break
			}
		}
	}
	if !slices.Contains(s.Durations, a.Seconds) {
		return fmt.Errorf("seconds %d is not one this model makes: %s", a.Seconds, ints(s.Durations))
	}
	if a.Size != "" {
		if len(s.Sizes) > 0 && !slices.Contains(s.Sizes, a.Size) {
			return fmt.Errorf("size %q is not one this model makes: %s", a.Size, strings.Join(s.Sizes, ", "))
		}
		if _, _, ok := dims(a.Size); !ok {
			return fmt.Errorf("size %q is not WIDTHxHEIGHT", a.Size)
		}
	} else {
		a.Resolution = strings.ToLower(strings.TrimSpace(a.Resolution))
		if a.Resolution == "" && len(s.Resolutions) > 0 {
			a.Resolution = s.Resolutions[0]
			if slices.Contains(s.Resolutions, "720p") {
				a.Resolution = "720p"
			}
		}
		if a.Resolution != "" && len(s.Resolutions) > 0 && !slices.Contains(s.Resolutions, a.Resolution) {
			return fmt.Errorf("resolution %q is not one this model makes: %s", a.Resolution, strings.Join(s.Resolutions, ", "))
		}
	}
	if a.Aspect != "" && len(s.Aspects) > 0 && !slices.Contains(s.Aspects, a.Aspect) {
		return fmt.Errorf("aspect_ratio %q is not one this model makes: %s", a.Aspect, strings.Join(s.Aspects, ", "))
	}
	if a.First != "" && !slices.Contains(s.Frames, "first_frame") {
		return fmt.Errorf("this model does not start from an image")
	}
	if a.Last != "" && !slices.Contains(s.Frames, "last_frame") {
		return fmt.Errorf("this model does not end on an image")
	}
	if a.Audio != nil && *a.Audio && s.Audio != nil && !*s.Audio {
		return fmt.Errorf("this model does not write audio")
	}
	return nil
}

// ceiling is the most one clip of a can cost at the vendor's price, in USD, and
// false for a model whose listed price cannot bound it. Every rate that can apply
// is considered and the dearest is taken: a rate for another resolution, for
// audio when none is written, or for a video input never sent does not apply.
func (s *videoSpec) ceiling(a videoAsk) (decimal.Decimal, bool) {
	res := a.Resolution
	if a.Size != "" {
		res = resolutionOf(a.Size)
	}
	audio := a.Audio == nil || *a.Audio
	images := len(a.References)
	if a.First != "" {
		images++
	}
	if a.Last != "" {
		images++
	}
	var second, token, image, floor decimal.Decimal
	var anySecond decimal.Decimal
	for k, v := range s.rates {
		usd := v
		if strings.HasPrefix(k, "cents_") {
			usd = v.Quo(decimal.New(100, 0), 18)
		}
		switch {
		case strings.Contains(k, "minimum"):
			floor = maxDecimal(floor, usd)
			continue
		case strings.Contains(k, "image"):
			image = maxDecimal(image, usd)
			continue
		case strings.Contains(k, "megapixel"), strings.Contains(k, "with_video_input"), strings.Contains(k, "continuation"):
			continue // a video input this API never sends
		}
		tokens := strings.Contains(k, "video_tokens")
		if !tokens {
			anySecond = maxDecimal(anySecond, usd)
		}
		if r := resolutionIn(k); r != "" && r != res {
			continue
		}
		if !audio && strings.Contains(k, "with_audio") {
			continue
		}
		if audio && strings.Contains(k, "without_audio") {
			continue
		}
		if strings.HasPrefix(k, "image_to_video") && a.First == "" {
			continue
		}
		if strings.HasPrefix(k, "text_to_video") && a.First != "" {
			continue
		}
		if strings.HasPrefix(k, "reference_") && images == 0 {
			continue
		}
		if tokens {
			token = maxDecimal(token, usd)
		} else {
			second = maxDecimal(second, usd)
		}
	}
	if second.IsZero() && token.IsZero() {
		second = anySecond // nothing names this ask: the dearest second it lists
	}
	secs := decimal.New(int64(a.Seconds), 0)
	total := second.Mul(secs)
	if !token.IsZero() {
		w, h := dimsOf(a, res)
		// A video token is a 32×32 patch of one of 24 frames a second.
		perSecond := decimal.New(int64(w*h*24), 0).Quo(decimal.New(1024, 0), 18)
		total = total.Add(token.Mul(perSecond).Mul(secs))
	}
	total = total.Add(image.Mul(decimal.New(int64(images), 0)))
	total = maxDecimal(total, floor)
	return total, total.Sign() > 0
}

// resolutionIn names the resolution a SKU key is for, "" for one for every
// resolution.
func resolutionIn(key string) string {
	for _, part := range strings.Split(key, "_") {
		if _, ok := resolutions[part]; ok {
			return part
		}
	}
	return ""
}

// resolutionOf names the resolution a WIDTHxHEIGHT size is: its shorter side.
func resolutionOf(size string) string {
	w, h, ok := dims(size)
	if !ok {
		return ""
	}
	short := min(w, h)
	best, gap := "", 1<<30
	for name, height := range resolutions {
		if name == "1k" {
			continue
		}
		if d := abs(height - short); d < gap {
			best, gap = name, d
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// dims reads a WIDTHxHEIGHT size.
func dims(size string) (int, int, bool) {
	ws, hs, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// dimsOf is the frame size an ask is made at: its size, or its resolution at its
// aspect ratio (16:9 when unstated), the larger dimension rounded up.
func dimsOf(a videoAsk, res string) (int, int) {
	if w, h, ok := dims(a.Size); ok {
		return w, h
	}
	short := resolutions[res]
	if short == 0 {
		short = 2160
	}
	aw, ah := 16, 9
	if x, y, ok := strings.Cut(a.Aspect, ":"); ok {
		if n, err := strconv.Atoi(x); err == nil && n > 0 {
			aw = n
		}
		if n, err := strconv.Atoi(y); err == nil && n > 0 {
			ah = n
		}
	}
	long := (short*max(aw, ah) + min(aw, ah) - 1) / min(aw, ah)
	if aw >= ah {
		return long, short
	}
	return short, long
}

func ints(v []int) string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, ", ")
}

// vendorBody is the body the vendor's POST /v1/videos is sent for a: OpenRouter's
// dialect, every field stated so the clip made is the clip reserved for.
func (a videoAsk) vendorBody(model, prompt string) ([]byte, error) {
	body := map[string]any{"model": model, "prompt": prompt, "duration": a.Seconds}
	if a.Size != "" {
		body["size"] = a.Size
	} else if a.Resolution != "" {
		body["resolution"] = a.Resolution
	}
	if a.Aspect != "" {
		body["aspect_ratio"] = a.Aspect
	}
	frame := func(url, kind string) map[string]any {
		return map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}, "frame_type": kind}
	}
	var frames []map[string]any
	if a.First != "" {
		frames = append(frames, frame(a.First, "first_frame"))
	}
	if a.Last != "" {
		frames = append(frames, frame(a.Last, "last_frame"))
	}
	if len(frames) > 0 {
		body["frame_images"] = frames
	}
	if len(a.References) > 0 {
		refs := make([]map[string]any, 0, len(a.References))
		for _, u := range a.References {
			refs = append(refs, map[string]any{"type": "image_url", "image_url": map[string]string{"url": u}})
		}
		body["input_references"] = refs
	}
	if a.Audio != nil {
		body["generate_audio"] = *a.Audio
	}
	if a.Seed != nil {
		body["seed"] = *a.Seed
	}
	return json.Marshal(body)
}
