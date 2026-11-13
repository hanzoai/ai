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
	"strings"
	"testing"
)

// TestFixturesSelfConsistent is the bedrock proof: every container built by
// fixtures.go must pass ValidateImage with EXACTLY the dimensions it was
// built at. If a fixture lies about its own size, no downstream verdict means
// anything.
func TestFixturesSelfConsistent(t *testing.T) {
	builders := map[string]func(w, h int) []byte{"image/png": PNG, "image/jpeg": JPEG, "image/gif": GIF, "image/webp": WEBP}
	sizes := []struct{ w, h int }{
		{1024, 1024}, // square
		{1792, 1024}, // landscape
		{1024, 1792}, // portrait
		{640, 480},   // custom 4:3
	}
	for mime, build := range builders {
		for _, s := range sizes {
			raw := build(s.w, s.h)
			gotMime, w, h, err := ValidateImage(raw, s.w, s.h)
			if err != nil {
				t.Errorf("%s %dx%d: %v", mime, s.w, s.h, err)
				continue
			}
			if gotMime != mime {
				t.Errorf("built %s but sniffed %s", mime, gotMime)
			}
			if w != s.w || h != s.h {
				t.Errorf("decoded %dx%d, built %dx%d", w, h, s.w, s.h)
			}
		}
	}
}

// TestValidateImageCatchesMismatch proves the dimension checker fails when
// pixels and request disagree — the aspect-ratio "silent ignore" class: a
// square artifact handed back for a portrait request must NOT pass.
func TestValidateImageCatchesMismatch(t *testing.T) {
	square := PNG(1024, 1024)
	if _, _, _, err := ValidateImage(square, 1024, 1792); err == nil {
		t.Error("square PNG accepted for a 1024x1792 request; the mismatch must fail")
	} else if !strings.Contains(err.Error(), "dimensions mismatch") || !strings.Contains(err.Error(), "portrait") {
		t.Errorf("error = %q, want it to name the mismatch and both aspects", err)
	}
	// No dimension demand (want 0): a valid image still validates on payload+MIME alone.
	if _, w, h, err := ValidateImage(square, 0, 0); err != nil || w != 1024 || h != 1024 {
		t.Errorf("ValidateImage(square, 0,0) = %d,%d,%v", w, h, err)
	}
}

// TestCorruptPayloadsFailEachTheirOwnWay proves each failure mode is caught
// with the RIGHT error — empty payload, non-image bytes, truncated stream.
func TestCorruptPayloadsFailEachTheirOwnWay(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string // substring the error must carry
	}{
		{"empty", nil, "empty payload"},
		{"html-error-page", NotAnImage(), "not a recognized image"},
		{"truncated-png", Truncate(PNG(640, 480), 12), "does not decode"},
		{"truncated-jpeg-header-only", Truncate(JPEG(640, 480), 2), "not a recognized image"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := ValidateImage(c.raw, 0, 0)
			if err == nil {
				t.Fatalf("%s passed validation", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
		})
	}
}

// TestSniffCatchesMislabel proves the MIME verdict comes from bytes, never
// from a label: PNG pixels are PNG whatever the sender claims.
func TestSniffCatchesMislabel(t *testing.T) {
	if got := SniffMIME(PNG(64, 64)); got != "image/png" {
		t.Errorf("sniff(png) = %q", got)
	}
	if got := SniffMIME([]byte("PK\x03\x04zipzip")); got != "" {
		t.Errorf("sniff(zip) = %q, want \"\" (a zip is not an image)", got)
	}
	// A JPEG missing its trailing 0xFF third byte must not sniff as JPEG.
	if got := SniffMIME([]byte{0xFF, 0xD8, 0x00}); got != "" {
		t.Errorf("sniff(broken jpeg magic) = %q, want \"\"", got)
	}
}

// TestAspectClassification pins the square/landscape/portrait wording used in
// every report line.
func TestAspectClassification(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want string
	}{
		{1024, 1024, "square"}, {1792, 1024, "landscape"}, {1024, 1792, "portrait"}, {640, 480, "landscape"}, {480, 640, "portrait"},
	} {
		if got := Aspect(tc.w, tc.h); got != tc.want {
			t.Errorf("Aspect(%d,%d) = %q, want %q", tc.w, tc.h, got, tc.want)
		}
	}
}
