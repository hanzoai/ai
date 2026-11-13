// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package imagegen

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ClampN mirrors the relay's own n clamp (controllers/images_api.go): n is
// forced into [1,10] before any cost math, so 0, -3 and 20 are all legal
// requests with defined outcomes — the checker asserts what SHOULD arrive,
// never what a buggy client sent.
func ClampN(n int) int { return min(max(n, 1), 10) }

// ParseSize reads an OpenAI-style "WxH" (e.g. "1024x1024") with the same
// tolerance as model.doaiImageSize: surrounding spaces and case are fine,
// anything unparseable ("square_hd", "1024", "0x0", "-1x10") is NOT ok — the
// relay then omits image_size and the model default silently applies, which
// this suite records as a finding rather than pretending the request was met.
func ParseSize(size string) (w, h int, ok bool) {
	size = strings.TrimSpace(size)
	if size == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	wi, werr := strconv.Atoi(strings.TrimSpace(parts[0]))
	hi, herr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if werr != nil || herr != nil || wi <= 0 || hi <= 0 {
		return 0, 0, false
	}
	return wi, hi, true
}

// CheckFalSubmit audits one captured POST /async-invoke body against the
// contract the relay promises fal (model/doai_image.go): the prompt must
// arrive byte-identical (prompt adherence is untestable if the relay mutates
// it), num_images must equal the clamped n, and a requested size must arrive
// as fal's structured image_size OBJECT — the bare "WxH" string form was
// rejected upstream ("image_size: Input should be a valid dictionary"), so a
// string here is the silent-ignore regression this issue hunts.
func CheckFalSubmit(raw []byte, wantModel, wantPrompt, wantSize string, wantN int) error {
	var body struct {
		ModelID string         `json:"model_id"`
		Input   map[string]any `json:"input"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("submit body is not JSON: %w", err)
	}
	if body.ModelID != wantModel {
		return fmt.Errorf("model_id = %q, want %q", body.ModelID, wantModel)
	}
	if body.Input == nil {
		return fmt.Errorf("submit body has no input block")
	}
	if got, _ := body.Input["prompt"].(string); got != wantPrompt {
		return fmt.Errorf("prompt not preserved verbatim: sent %q, want %q", clip(got), clip(wantPrompt))
	}
	nf, _ := body.Input["num_images"].(float64)
	if int(nf) != ClampN(wantN) {
		return fmt.Errorf("num_images = %v, want the clamped %d", body.Input["num_images"], ClampN(wantN))
	}

	wantW, wantH, sized := ParseSize(wantSize)
	is, present := body.Input["image_size"]
	if !sized {
		// No usable size was requested: image_size must be ABSENT (omitempty),
		// not sent as a junk value that fal would reject.
		if present {
			return fmt.Errorf("image_size = %v sent for an unparseable size %q; it must be omitted so the model default applies", is, wantSize)
		}
		return nil
	}
	if !present {
		return fmt.Errorf("requested size %s silently dropped: image_size absent from submit", wantSize)
	}
	obj, ok := is.(map[string]any)
	if !ok {
		return fmt.Errorf("image_size = %v (%T), want fal's {width,height} object — the bare string form fails upstream validation", is, is)
	}
	gotW, _ := obj["width"].(float64)
	gotH, _ := obj["height"].(float64)
	if int(gotW) != wantW || int(gotH) != wantH {
		return fmt.Errorf("image_size = {%v,%v}, want {%d,%d} for %s", obj["width"], obj["height"], wantW, wantH, wantSize)
	}
	return nil
}

// GenImage is one entry of the OpenAI images "data" array.
type GenImage struct {
	URL     string `json:"url"`
	B64JSON string `json:"b64_json"`
}

// CheckGatewayReply audits a /v1/images/generations response: 200, the OpenAI
// shape (created > 0, data non-empty), and every entry carrying a usable
// payload — url or b64_json, never neither (a silent empty artifact) and a
// b64 payload must base64-decode to actual bytes. Returns the entries so live
// lanes can download/validate each one.
func CheckGatewayReply(status int, raw []byte) ([]GenImage, error) {
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", status, clip(string(raw)))
	}
	var out struct {
		Created int64      `json:"created"`
		Data    []GenImage `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("reply is not the OpenAI images shape: %w (%s)", err, clip(string(raw)))
	}
	if out.Created <= 0 {
		return nil, fmt.Errorf("created = %d, want a positive unix timestamp", out.Created)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("data array is empty: no images were produced")
	}
	for i, im := range out.Data {
		if im.URL == "" && im.B64JSON == "" {
			return nil, fmt.Errorf("data[%d] has neither url nor b64_json — an entry with no payload", i)
		}
		if im.B64JSON != "" {
			dec, err := base64.StdEncoding.DecodeString(im.B64JSON)
			if err != nil {
				return nil, fmt.Errorf("data[%d].b64_json is not valid base64: %w", i, err)
			}
			if len(dec) == 0 {
				return nil, fmt.Errorf("data[%d].b64_json decodes to an empty payload", i)
			}
		}
	}
	return out.Data, nil
}

// clip bounds a detail string so one verbose body cannot bloat the report.
func clip(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
