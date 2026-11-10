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
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
)

// Every fixture is built byte-for-byte at run time — no binary blobs in git,
// and every validator proof can name the exact dimensions it was fed.

// frame draws deterministic noise so no codec can pass by compressing a flat
// fill into nothing; the same seed always paints the same pixels.
func frame(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(int64(w*100003 + h)))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 255})
		}
	}
	return img
}

// PNG encodes a w×h image as a real, decodable PNG.
func PNG(w, h int) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, frame(w, h)); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// JPEG encodes a w×h image as a real, decodable JPEG.
func JPEG(w, h int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, frame(w, h), &jpeg.Options{Quality: 60}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// GIF encodes a w×h image as a real, decodable GIF.
func GIF(w, h int) []byte {
	var buf bytes.Buffer
	if err := gif.Encode(&buf, frame(w, h), nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// WEBP builds a minimal VP8X (extended-format) WebP declaring a w×h canvas.
// The stdlib decodes dimensions from the VP8X header; building the container
// by hand keeps the fixture exact-size without pulling an encoder in.
func WEBP(w, h int) []byte {
	payload := make([]byte, 10) // flags(1) + reserved(3) + canvas w-1(3) + canvas h-1(3)
	putLE24(payload[4:], uint32(w-1))
	putLE24(payload[7:], uint32(h-1))
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(4+8+len(payload))) // "WEBP" + chunk header + payload
	buf.WriteString("WEBP")
	buf.WriteString("VP8X")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(payload)))
	buf.Write(payload)
	return buf.Bytes()
}

func putLE24(p []byte, v uint32) { p[0] = byte(v); p[1] = byte(v >> 8); p[2] = byte(v >> 16) }

// Truncate drops the tail of a container so a decoder meets EOF mid-stream —
// the "silent partial artifact" class.
func Truncate(raw []byte, keep int) []byte { return raw[:min(len(raw), keep)] }

// NotAnImage is what a CDN hands back when generation failed upstream but
// the URL still answers 200: an HTML error page where pixels were promised.
func NotAnImage() []byte {
	return []byte("<!DOCTYPE html>\n<html><head><title>404 Not Found</title></head><body>Not Found</body></html>")
}
