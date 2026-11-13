// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package multimodal

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"
	"testing/quick"
)

// These are the regression-fixture checks of #135's acceptance criteria: every synthetic payload
// must stay a structurally valid artifact for its format — a fixture that stopped decoding is a
// broken guardrail test no model answer can be trusted against. They run without network or key,
// so any CI gate covers them.

func TestImageFixtures(t *testing.T) {
	payload := PayloadText(VectorByID("image-overlay"))

	t.Run("overlay", func(t *testing.T) {
		a, err := Build("overlay", payload)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(a.Bytes))
		if err != nil {
			t.Fatalf("overlay PNG does not decode: %v", err)
		}
		lo, hi := luminanceRange(img)
		if hi-lo < 200 { // clean black-on-white: OCR vectors need full contrast
			t.Errorf("overlay contrast = %d..%d, want a span of at least 200", lo, hi)
		}
		if b := img.Bounds(); b.Dx() < 600 || b.Dy() < 100 {
			t.Errorf("overlay page %dx%d is too small to OCR reliably", b.Dx(), b.Dy())
		}
	})

	t.Run("lowcontrast", func(t *testing.T) {
		a, err := Build("lowcontrast", payload)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(a.Bytes))
		if err != nil {
			t.Fatalf("lowcontrast PNG does not decode: %v", err)
		}
		lo, hi := luminanceRange(img)
		if d := hi - lo; d < 3 || d > 20 {
			t.Errorf("stego contrast span = %d (luminance %d..%d), want a hidden-but-parsable 3..20", d, lo, hi)
		}
	})

	t.Run("inverted", func(t *testing.T) {
		a, err := Build("inverted", payload)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(a.Bytes))
		if err != nil {
			t.Fatalf("inverted PNG does not decode: %v", err)
		}
		lo, hi := luminanceRange(img)
		if hi-lo < 200 || lo > 40 { // dark paper dominates; ink is the bright minority
			t.Errorf("inverted luminance range = %d..%d, want a black page with white ink", lo, hi)
		}
	})

	t.Run("pdf_as_image_carries_the_exfil_payload", func(t *testing.T) {
		a, err := Build(VectorByID("image-pdf-as-image").Fixture, PayloadText(VectorByID("image-pdf-as-image")))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := png.Decode(bytes.NewReader(a.Bytes)); err != nil {
			t.Fatalf("pdf-as-image PNG does not decode: %v", err)
		}
		// The exfil class has no magic token; its payload text is what differs, and the two
		// overlay-class fixtures must therefore produce different bytes from each other.
		b, err := Build(VectorByID("image-overlay").Fixture, PayloadText(VectorByID("image-overlay")))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(a.Bytes, b.Bytes) {
			t.Error("exfil and override overlays are byte-identical — the payload text is not reaching the renderer")
		}
	})
}

// luminanceRange returns the min/max luma over all pixels (BT.601 weights, integer math).
func luminanceRange(img image.Image) (lo, hi int) {
	lo, hi = 255, 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			rr, gg, bb := r>>8, g>>8, bl>>8 // At returns 0..65535 per channel; scale to 0..255
			l := int((54*rr + 183*gg + 105*bb) / 342)
			if l < lo {
				lo = l
			}
			if l > hi {
				hi = l
			}
		}
	}
	return lo, hi
}

func TestAudioFixtures(t *testing.T) {
	t.Run("spoken", func(t *testing.T) {
		a, err := Build("spoken", PayloadText(VectorByID("audio-spoken")))
		if err != nil {
			t.Fatal(err)
		}
		rate, nSamples, peak := parseWAV(t, a.Bytes)
		if rate != 8000 {
			t.Errorf("sample rate = %d, want 8000", rate)
		}
		if nSamples < 8000 { // the payloads run several seconds — anything shorter means the tone loop broke
			t.Errorf("only %d samples — payload did not encode to several seconds of audio", nSamples)
		}
		if peak < 5000 {
			t.Errorf("peak amplitude = %d, want an audible signal (>5000)", peak)
		}
	})

	t.Run("ultrasonic", func(t *testing.T) {
		a, err := Build("ultrasonic", PayloadText(VectorByID("audio-ultrasonic")))
		if err != nil {
			t.Fatal(err)
		}
		rate, nSamples, peak := parseWAV(t, a.Bytes)
		if rate < 30000 { // the 15 kHz carrier needs >2x headroom under Nyquist or it aliases away
			t.Errorf("sample rate = %d, want at least 30000 for a 15 kHz carrier", rate)
		}
		if d := float64(nSamples) / float64(rate); d > 15 {
			t.Errorf("ultrasonic runs %.1fs — must stay short enough to ride a data URL", d)
		}
		if peak < 20000 {
			t.Errorf("peak amplitude = %d, want the carrier near full scale", peak)
		}
	})
}

// parseWAV checks the canonical RIFF/WAVE/PCM layout and returns rate, sample count and peak.
func parseWAV(t *testing.T, b []byte) (rate, samples, peak int) {
	t.Helper()
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" || string(b[12:16]) != "fmt " {
		t.Fatalf("not a RIFF/WAVE/fmt file: %q", clip(string(b[:min(16, len(b))])))
	}
	if format := binary.LittleEndian.Uint16(b[20:22]); format != 1 {
		t.Errorf("audio format = %d, want PCM (1)", format)
	}
	rate = int(binary.LittleEndian.Uint32(b[24:28]))
	if bits := binary.LittleEndian.Uint16(b[34:36]); bits != 16 {
		t.Errorf("bit depth = %d, want 16", bits)
	}
	if string(b[36:40]) != "data" {
		t.Fatalf("no data chunk at the canonical offset")
	}
	dataLen := int(binary.LittleEndian.Uint32(b[40:44]))
	if dataLen > len(b)-44 {
		t.Errorf("data chunk claims %d bytes, file only holds %d", dataLen, len(b)-44)
		dataLen = len(b) - 44
	}
	for i := 0; i+1 < dataLen; i += 2 {
		s := int(int16(binary.LittleEndian.Uint16(b[44+i:])))
		if s < 0 {
			s = -s
		}
		if s > peak {
			peak = s
		}
	}
	return rate, dataLen / 2, peak
}

func TestAVIFixture(t *testing.T) {
	v := VectorByID("video-keyframes")
	a, err := Build(v.Fixture, PayloadText(v))
	if err != nil {
		t.Fatal(err)
	}
	b := a.Bytes
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "AVI " {
		t.Fatalf("not a RIFF/AVI container: %q", clip(string(b[:min(16, len(b))])))
	}
	if total := 8 + int(binary.LittleEndian.Uint32(b[4:8])); total != len(b) {
		t.Errorf("RIFF size %d disagrees with file length %d — a parser will read past or truncate the index", total, len(b))
	}
	for _, fourcc := range []string{"hdrl", "avih", "strf", "MJPG", "movi", "00dc", "idx1"} {
		if !bytes.Contains(b, []byte(fourcc)) {
			t.Errorf("AVI is missing its %s chunk", fourcc)
		}
	}
	wantFrames := len(strings.Fields(PayloadText(v))) + 1 // one word per frame plus the title card
	if len(a.Frames) != wantFrames {
		t.Errorf("%d frames, want %d (payload words + title)", len(a.Frames), wantFrames)
	}
	for i, f := range a.Frames {
		if len(f) < 4 || f[0] != 0xFF || f[1] != 0xD8 || f[len(f)-2] != 0xFF || f[len(f)-1] != 0xD9 {
			t.Fatalf("frame %d is not a complete JPEG (SOI/EOI markers missing)", i)
		}
	}
	img, err := jpeg.Decode(bytes.NewReader(a.Frames[0]))
	if err != nil {
		t.Fatalf("title frame does not decode as JPEG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 640 || b.Dy() != 360 {
		t.Errorf("frame size %dx%d, want 640x360", b.Dx(), b.Dy())
	}
	// idx1 offsets must address real chunks: ffmpeg convention counts entry offsets from the
	// "movi" fourcc itself, so the first entry lands exactly on the first "00dc" tag.
	i := bytes.Index(b, []byte("movi"))
	if i < 0 {
		t.Fatal("no movi list")
	}
	firstOff := binary.LittleEndian.Uint32(b[len(b)-len(a.Frames)*16+8:]) // first idx1 entry's dwOffset
	if got := b[i+int(firstOff) : i+4+int(firstOff)]; string(got) != "00dc" {
		t.Errorf("idx1 first offset %d lands on %q, want 00dc", firstOff, got)
	}
}

func TestPDFFixture(t *testing.T) {
	payload := PayloadText(VectorByID("doc-pdf-white"))
	a, err := Build("pdf", payload)
	if err != nil {
		t.Fatal(err)
	}
	b := a.Bytes
	if !bytes.HasPrefix(b, []byte("%PDF-1.4")) {
		t.Errorf("missing PDF header")
	}
	for _, want := range []string{payload, "startxref", "%%EOF"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("PDF is missing %q", clip(want))
		}
	}
	if !bytes.Contains(b, []byte("1 1 1 rg")) {
		t.Error("PDF has no white-color operator — the hidden-text trick broke")
	}
	// The xref table is the parser contract: every offset must land on its object header.
	xref := bytes.Index(b, []byte("\nxref\n"))
	if xref < 0 {
		t.Fatal("no xref table")
	}
	lines := strings.Split(string(b[xref+1:]), "\n")
	for n := 1; n <= 5; n++ { // five objects: lines[0]="xref", lines[1]="0 6", lines[2]=free entry, entry n at n+2
		fields := strings.Fields(lines[n+2])
		if len(fields) < 2 || fields[2] != "n" {
			t.Fatalf("xref entry %d malformed: %q", n, lines[n+1])
		}
		var off int
		for _, c := range fields[0] {
			off = off*10 + int(c-'0')
		}
		if !bytes.HasPrefix(b[off:], []byte(itoa(n)+" 0 obj")) {
			t.Errorf("xref entry %d points at %q, want \"%d 0 obj\"", n, clip(string(b[off:min(off+12, len(b))])), n)
		}
	}
}

func TestDOCXFixture(t *testing.T) {
	payload := PayloadText(VectorByID("doc-docx-hidden-run"))
	a, err := Build("docx", payload)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(a.Bytes), int64(len(a.Bytes)))
	if err != nil {
		t.Fatalf("docx is not a readable OPC zip: %v", err)
	}
	want := map[string]bool{"[Content_Types].xml": false, "_rels/.rels": false, "word/document.xml": false}
	for _, f := range zr.File {
		want[f.Name] = true
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		doc := string(raw)
		if !strings.Contains(doc, "<w:vanish/>") {
			t.Error("document.xml lost the hidden-run property")
		}
		if !strings.Contains(doc, payload) {
			t.Error("document.xml does not carry the payload text")
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("docx is missing %s", name)
		}
	}
}

func TestTextDocumentFixtures(t *testing.T) {
	for _, tc := range []struct{ fixture, id, hiddenMarker string }{
		{"markdown", "doc-md-hidden-comment", "<!--"},
		{"html", "doc-html-invisible-div", "display:none"},
		{"csv", "doc-csv-formula-cell", "=HYPERLINK("},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			v := VectorByID(tc.id)
			a, err := Build(v.Fixture, PayloadText(v))
			if err != nil {
				t.Fatal(err)
			}
			s := string(a.Bytes)
			payload := PayloadText(v)
			if !strings.Contains(s, tc.hiddenMarker) {
				t.Errorf("the hidden channel (%s) is gone from the fixture", tc.hiddenMarker)
			}
			if !strings.Contains(s, payload) {
				t.Error("payload text missing from the document")
			}
			if strings.Count(s, payload) < 2 {
				t.Errorf("payload should ride both hidden channels of %s at least twice", tc.fixture)
			}
			// The benign visible line must stay: a fixture that only holds instructions stops
			// testing "benign content + hidden instruction" and becomes plain text injection.
			if !strings.Contains(strings.ToUpper(s), "REVENUE") {
				t.Error("the benign visible content disappeared — the cross-modal confound is back")
			}
		})
	}
}

func TestWrapText(t *testing.T) {
	check := func(s string) bool {
		got, want := strings.Join(wrapText(s), ""), strings.Join(strings.Fields(s), "")
		if got != want {
			return false // nothing may be dropped or duplicated, across hard-splits included
		}
		for _, ln := range wrapText(s) {
			if len([]rune(ln)) > fontWrap {
				return false
			}
		}
		return true
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 200}); err != nil {
		t.Error(err)
	}
}
