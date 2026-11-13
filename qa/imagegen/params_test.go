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
	"strings"
	"testing"
)

// TestClampN pins the relay's [1,10] clamp contract.
func TestClampN(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 1}, {-3, 1}, {1, 1}, {7, 7}, {10, 10}, {11, 10}, {20, 10}} {
		if got := ClampN(tc.in); got != tc.want {
			t.Errorf("ClampN(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestParseSize mirrors model.doaiImageSize's tolerance and rejection set —
// the two parsers must agree or the suite audits a different contract than
// the relay implements.
func TestParseSize(t *testing.T) {
	for _, tc := range []struct {
		size string
		w, h int
		ok   bool
	}{
		{"1024x1024", 1024, 1024, true},
		{"1792x1024", 1792, 1024, true},
		{"  1024 x 1024  ", 1024, 1024, true}, // the relay trims spaces too
		{"", 0, 0, false},
		{"square_hd", 0, 0, false},
		{"1024", 0, 0, false},
		{"0x0", 0, 0, false},
		{"-1x10", 0, 0, false},
	} {
		w, h, ok := ParseSize(tc.size)
		if w != tc.w || h != tc.h || ok != tc.ok {
			t.Errorf("ParseSize(%q) = %d,%d,%v, want %d,%d,%v", tc.size, w, h, ok, tc.w, tc.h, tc.ok)
		}
	}
}

const conformPrompt = "a photorealistic red fox sitting in fresh snow at golden hour, shallow depth of field"

// TestCheckFalSubmitConforming proves the checker ACCEPTS a body that follows
// model/doai_image.go's contract exactly: fal's structured image_size object,
// verbatim prompt, clamped num_images.
func TestCheckFalSubmitConforming(t *testing.T) {
	body := `{"model_id":"fal-ai/flux/schnell","input":{"prompt":"` + conformPrompt + `",` +
		`"num_images":3,"image_size":{"width":1024,"height":1792},"enable_safety_checker":true}}`
	if err := CheckFalSubmit([]byte(body), "fal-ai/flux/schnell", conformPrompt, "1024x1792", 3); err != nil {
		t.Errorf("conforming body rejected: %v", err)
	}
	// No size requested → image_size absent is conforming.
	bodyNoSize := `{"model_id":"fal-ai/fast-sdxl","input":{"prompt":"` + conformPrompt + `","num_images":1}}`
	if err := CheckFalSubmit([]byte(bodyNoSize), "fal-ai/fast-sdxl", conformPrompt, "", 1); err != nil {
		t.Errorf("conforming size-less body rejected: %v", err)
	}
	// n=0 must be checked against the CLAMPED 1, not the raw 0.
	bodyClamped := `{"model_id":"fal-ai/flux/schnell","input":{"prompt":"` + conformPrompt + `","num_images":1}}`
	if err := CheckFalSubmit([]byte(bodyClamped), "fal-ai/flux/schnell", conformPrompt, "", 0); err != nil {
		t.Errorf("clamped n=0 body rejected: %v", err)
	}
}

// TestCheckFalSubmitCatchesDivergence proves the checker REJECTS each silent-
// ignore / mutation class, each with a named reason. These are the canned
// divergent twins of the conforming body above.
func TestCheckFalSubmitCatchesDivergence(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		size       string
		n          int
		wantErrSub string
	}{
		{
			name: "size-as-bare-string (the historical fal regression)",
			body: `{"model_id":"fal-ai/flux/schnell","input":{"prompt":"` + conformPrompt + `","num_images":1,"image_size":"1024x1792"}}`,
			size: "1024x1792", n: 1,
			wantErrSub: "{width,height} object",
		},
		{
			name: "size-silently-dropped",
			body: `{"model_id":"fal-ai/flux/schnell","input":{"prompt":"` + conformPrompt + `","num_images":1}}`,
			size: "1024x1792", n: 1,
			wantErrSub: "silently dropped",
		},
		{
			name: "size-dimensions-swapped",
			body: `{"model_id":"fal-ai/flux/schnell","input":{"prompt":"` + conformPrompt + `","num_images":1,"image_size":{"width":1792,"height":1024}}}`,
			size: "1024x1792", n: 1,
			wantErrSub: "want {1024,1792}",
		},
		{
			name: "prompt-mutated",
			body: `{"model_id":"fal-ai/flux/schnell","input":{"prompt":"a red fox","num_images":1}}`,
			size: "", n: 1,
			wantErrSub: "not preserved verbatim",
		},
		{
			name: "num-images-unclamped",
			body: `{"model_id":"fal-ai/flux/schnell","input":{"prompt":"` + conformPrompt + `","num_images":13}}`,
			size: "", n: 13,
			wantErrSub: "want the clamped 10",
		},
		{
			name: "wrong-model-id",
			body: `{"model_id":"fal-ai/flux/dev","input":{"prompt":"` + conformPrompt + `","num_images":1}}`,
			size: "", n: 1,
			wantErrSub: `model_id = "fal-ai/flux/dev"`,
		},
		{
			name: "junk-size-sent-anyway",
			body: `{"model_id":"fal-ai/flux/schnell","input":{"prompt":"` + conformPrompt + `","num_images":1,"image_size":{"width":640,"height":480}}}`,
			size: "square_hd", n: 1,
			wantErrSub: "must be omitted",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckFalSubmit([]byte(c.body), "fal-ai/flux/schnell", conformPrompt, c.size, c.n)
			if err == nil {
				t.Fatalf("divergent body accepted: %s", c.body)
			}
			if !strings.Contains(err.Error(), c.wantErrSub) {
				t.Errorf("error = %q, want it to mention %q", err, c.wantErrSub)
			}
		})
	}
}

// TestCheckGatewayReplyConformingAndDivergent walks the OpenAI images reply
// shape: conforming url and b64 bodies pass; empty data, payload-less entries
// and non-decodable base64 each fail with a named reason.
func TestCheckGatewayReply(t *testing.T) {
	ok := `{"created":1730000000,"data":[{"url":"https://v3b.fal.media/files/x.jpg"}]}`
	data, err := CheckGatewayReply(200, []byte(ok))
	if err != nil || len(data) != 1 || data[0].URL == "" {
		t.Fatalf("conforming url reply rejected: %v (%v)", err, data)
	}
	// A real b64 payload (an actual PNG, not a word pretending to be base64).
	b64 := `{"created":1730000000,"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(PNG(64, 64)) + `"}]}`
	data, err = CheckGatewayReply(200, []byte(b64))
	if err != nil || len(data) != 1 {
		t.Fatalf("conforming b64 reply rejected: %v", err)
	}

	bad := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{"empty-data", 200, `{"created":1730000000,"data":[]}`, "empty"},
		{"entry-without-payload", 200, `{"created":1730000000,"data":[{"url":"","b64_json":""}]}`, "neither url nor b64_json"},
		{"invalid-base64", 200, `{"created":1,"data":[{"b64_json":"!!!not base64!!!"}]}`, "not valid base64"},
		{"zero-created", 200, `{"created":0,"data":[{"url":"https://x/y.png"}]}`, "created = 0"},
		{"not-json", 200, `<html>502 Bad Gateway</html>`, "not the OpenAI images shape"},
		{"http-error", 429, `{"error":{"message":"rate limited"}}`, "HTTP 429"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := CheckGatewayReply(c.status, []byte(c.body)); err == nil {
				t.Fatalf("reply accepted: %s", c.body)
			} else if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err, c.wantSub)
			}
		})
	}
}
