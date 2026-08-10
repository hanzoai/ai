// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package web

// The request-body bound, asserted against the shape that defeats it.
//
// CopyBody runs in ServeHTTP above the filter chain, so it runs before
// authentication: whatever it allocates, an anonymous caller can make it
// allocate. That is why the bound is on the DECOMPRESSED reader and why these
// tests measure what CopyBody RETURNS rather than what the client SENT.

import (
	"bytes"
	"compress/gzip"
	"net/http/httptest"
	"runtime"
	"testing"
)

// gzipBomb returns a gzip stream that decompresses to plainBytes of zeros.
func gzipBomb(t *testing.T, plainBytes int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	chunk := make([]byte, 1<<20)
	for written := 0; written < plainBytes; written += len(chunk) {
		if _, err := zw.Write(chunk); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// TestCopyBodyBoundsDecompressed proves the bound holds against a gzip body,
// which is the only shape that can defeat it: DEFLATE's ratio ceiling is about
// 1030:1, so a bound applied to the COMPRESSED stream lets a few hundred KiB on
// the wire become hundreds of MiB in this process — unauthenticated, on every
// endpoint, because CopyBody runs above the filter chain.
//
// The assertion is on BYTES ALLOCATED, not on the bytes returned. Returned
// length cannot see this bug: a body read in full and then trimmed to the bound
// returns exactly what a body never read past the bound returns. The whole
// vulnerability is the memory committed in between, so that is what is measured.
func TestCopyBodyBoundsDecompressed(t *testing.T) {
	const limit = 1 << 20   // 1 MiB
	const plain = 256 << 20 // what the bomb expands to
	bomb := gzipBomb(t, plain)

	r := httptest.NewRequest("POST", "/v1/audio/speech", bytes.NewReader(bomb))
	r.Header.Set("Content-Encoding", "gzip")
	ctx := NewContext()
	ctx.Reset(httptest.NewRecorder(), r)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := ctx.Input.CopyBody(limit)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	// Reading to the bound costs a few MiB of buffer growth; reading the whole
	// bomb costs at least the 256 MiB it expands to. Nothing lands between.
	const ceiling = 32 << 20
	if allocated > ceiling {
		t.Fatalf("CopyBody allocated %d MiB expanding a %d KiB gzip body under a %d MiB bound "+
			"(the bomb expands to %d MiB) — the bound is not on the decompressed reader",
			allocated>>20, len(bomb)>>10, limit>>20, plain>>20)
	}
	if int64(len(got)) > limit {
		t.Errorf("CopyBody returned %d MiB, past the %d MiB bound", len(got)>>20, limit>>20)
	}
	if !ctx.Input.BodyTooLarge {
		t.Error("an over-limit gzip body was not flagged BodyTooLarge, so the router cannot refuse it")
	}
}

// TestCopyBodyFlagsOversizePlain proves an over-limit plain body is REPORTED
// rather than silently truncated. Truncation is the failure mode that hides the
// cause: the handler receives a body that ends mid-structure and blames whatever
// field went missing, so the caller is told the wrong thing.
func TestCopyBodyFlagsOversizePlain(t *testing.T) {
	const limit = 1 << 20
	body := bytes.Repeat([]byte("A"), 8<<20)

	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", bytes.NewReader(body))
	ctx := NewContext()
	ctx.Reset(httptest.NewRecorder(), r)

	got := ctx.Input.CopyBody(limit)
	if !ctx.Input.BodyTooLarge {
		t.Fatalf("an %d MiB body under a %d MiB bound was not flagged oversize", len(body)>>20, limit>>20)
	}
	if int64(len(got)) > limit {
		t.Errorf("CopyBody kept %d bytes, past the %d bound", len(got), limit)
	}
}

// TestCopyBodyAtLimitAccepted proves the bound is a ceiling and not an
// off-by-one that refuses the largest legal body. This is the mutation a `>=`
// would introduce, and the reason CopyBody reads one byte past the bound.
func TestCopyBodyAtLimitAccepted(t *testing.T) {
	const limit = 1 << 20
	body := bytes.Repeat([]byte("A"), limit)

	r := httptest.NewRequest("POST", "/v1/audio/transcriptions", bytes.NewReader(body))
	ctx := NewContext()
	ctx.Reset(httptest.NewRecorder(), r)

	got := ctx.Input.CopyBody(limit)
	if ctx.Input.BodyTooLarge {
		t.Fatal("a body of EXACTLY the bound was refused; the bound is off by one")
	}
	if len(got) != limit {
		t.Errorf("CopyBody returned %d bytes, want the full %d", len(got), limit)
	}
}

// TestServeHTTPRefusesOversizeBody proves the router answers 413 and names the
// limit, and that it does so ABOVE the handler — the handler must never see a
// truncated body at all.
func TestServeHTTPRefusesOversizeBody(t *testing.T) {
	p := NewRouter()
	p.maxMemory = 1 << 20

	r := httptest.NewRequest("POST", "/v1/audio/transcriptions",
		bytes.NewReader(bytes.Repeat([]byte("A"), 4<<20)))
	rw := httptest.NewRecorder()
	p.ServeHTTP(rw, r)

	if rw.Code != 413 {
		t.Fatalf("status = %d, want 413 for a 4 MiB body under a 1 MiB bound", rw.Code)
	}
	if body := rw.Body.String(); !bytes.Contains([]byte(body), []byte("1 MiB")) {
		t.Errorf("refusal %q does not name the limit — a caller cannot act on it", body)
	}
}

// TestServeHTTPRefusesGzipBomb is the same refusal for the compressed shape,
// end to end through the router: the bomb never reaches a handler.
func TestServeHTTPRefusesGzipBomb(t *testing.T) {
	p := NewRouter()
	p.maxMemory = 1 << 20

	r := httptest.NewRequest("POST", "/v1/audio/speech", bytes.NewReader(gzipBomb(t, 64<<20)))
	r.Header.Set("Content-Encoding", "gzip")
	rw := httptest.NewRecorder()
	p.ServeHTTP(rw, r)

	if rw.Code != 413 {
		t.Fatalf("status = %d, want 413 for a gzip bomb", rw.Code)
	}
}
