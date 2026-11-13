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
	"bytes"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"  // registers GIF for image.DecodeConfig
	_ "image/jpeg" // registers JPEG for image.DecodeConfig
	_ "image/png"  // registers PNG for image.DecodeConfig

	_ "golang.org/x/image/webp" // registers WebP for image.DecodeConfig
)

// SniffMIME identifies an image container from its magic bytes alone — the
// label the upstream prints is never consulted, because the failure mode this
// suite hunts includes a URL that serves an HTML error page (or a zip) while
// still answering 200. Returns "" when the bytes are not a known image.
func SniffMIME(raw []byte) string {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], pngMagic):
		return "image/png"
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "image/jpeg"
	case len(raw) >= 6 && (bytes.Equal(raw[:6], []byte("GIF87a")) || bytes.Equal(raw[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(raw) >= 12 && bytes.Equal(raw[:4], []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}

// ValidateImage is the acceptance-criteria checker #131 asks for: a
// non-empty payload, a recognized MIME sniffed from the bytes, a header that
// actually decodes, and — when wantW/wantH are given — decoded dimensions
// exactly matching what was requested. Any parameter mismatch or silent
// ignore surfaces here as a named error, never as a passing check.
func ValidateImage(raw []byte, wantW, wantH int) (mime string, w, h int, err error) {
	if len(raw) == 0 {
		return "", 0, 0, fmt.Errorf("empty payload: the upstream produced no bytes")
	}
	mime = SniffMIME(raw)
	if mime == "" {
		n := min(len(raw), 16)
		return "", 0, 0, fmt.Errorf("payload is not a recognized image (%d bytes, starts %q): %s",
			len(raw), hex.EncodeToString(raw[:n]), firstWords(raw))
	}
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(raw))
	if derr != nil {
		return mime, 0, 0, fmt.Errorf("payload sniffs as %s but its header does not decode: %w", mime, derr)
	}
	w, h = cfg.Width, cfg.Height
	if w <= 0 || h <= 0 {
		return mime, w, h, fmt.Errorf("%s header declares degenerate dimensions %dx%d", mime, w, h)
	}
	if wantW > 0 && wantH > 0 && (w != wantW || h != wantH) {
		return mime, w, h, fmt.Errorf("dimensions mismatch: requested %dx%d (%s), got %dx%d (%s)",
			wantW, wantH, Aspect(wantW, wantH), w, h, Aspect(w, h))
	}
	return mime, w, h, nil
}

// Aspect classifies a frame the way the issue's resolution matrix words it.
func Aspect(w, h int) string {
	switch {
	case w == h:
		return "square"
	case w > h:
		return "landscape"
	default:
		return "portrait"
	}
}

// firstWords quotes up to 48 printable bytes of a payload for the report
// trace — enough to recognize an HTML error page at a glance.
func firstWords(raw []byte) string {
	s := string(raw[:min(len(raw), 48)])
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r > 0x7E {
			b = append(b, '.')
			continue
		}
		b = append(b, r)
	}
	return string(b)
}
