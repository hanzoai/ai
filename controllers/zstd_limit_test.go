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

package controllers

// The zstd bound on /v1/responses, asserted against the shape that defeats it.
//
// The socket bounds every body at zip.Config.BodyLimit, so the input reaching this
// decoder is already capped. That cap bounds nothing that costs memory: zstd's
// ratio on repetitive input runs to thousands to one, and the decoder's own
// default ceiling is 64 GiB. The bound has to be on the OUTPUT.
//
// Both transports reach this decoder before the token is validated — the HTTP
// handler checks only that the Authorization header starts with "Bearer ", and
// the ZAP handler sniffs the zstd magic on any body at all — so whatever it
// allocates, a caller holding no valid credential can make it allocate.

import (
	"errors"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// zstdBomb returns a zstd frame that decompresses to plainBytes of zeros.
func zstdBomb(t *testing.T, plainBytes int) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	defer enc.Close()
	return enc.EncodeAll(make([]byte, plainBytes), nil)
}

// TestDecodeResponsesZstdBoundsOutput proves the decoder refuses a frame that
// expands past MaxDecoded, and refuses it WITHOUT allocating what it would
// have expanded to.
//
// The assertion is on BYTES ALLOCATED, not on the error. An error returned
// after the memory was already committed is the bug, not the fix — the pod is
// dead either way — so the memory is what gets measured.
func TestDecodeResponsesZstdBoundsOutput(t *testing.T) {
	// One byte past the bound is still over it, but building a bomb that big
	// would cost the test what it is asserting nobody pays. 4x the bound is
	// unambiguously over and cheap to construct.
	plain := int(MaxDecoded) * 4
	bomb := zstdBomb(t, plain)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := decodeResponsesZstd(bomb)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	if err == nil {
		t.Fatalf("a %d MiB expansion under a %d MiB bound was accepted (%d MiB returned)",
			plain>>20, MaxDecoded>>20, len(got)>>20)
	}
	if !errors.Is(err, zstd.ErrDecoderSizeExceeded) {
		t.Fatalf("err = %v, want ErrDecoderSizeExceeded — callers key 413 off that "+
			"error, and any other error is reported 400 instead", err)
	}
	// Refusing costs the decoder's window and buffers, not the expansion. Reading
	// the whole bomb costs at least the 256 MiB it expands to. Nothing lands
	// between, so this ceiling separates the fix from the bug.
	ceiling := uint64(MaxDecoded)
	if allocated > ceiling {
		t.Fatalf("decode allocated %d MiB refusing a %d KiB frame that expands to %d MiB "+
			"under a %d MiB bound — the bound is not on the decoder's output",
			allocated>>20, len(bomb)>>10, plain>>20, MaxDecoded>>20)
	}
}

// TestDecodeResponsesZstdRoundTrips proves a legitimate body under the bound is
// unaffected — the bound refuses bombs, not clients. Codex compresses real
// Responses requests with zstd, so this is the path that must not regress.
func TestDecodeResponsesZstdRoundTrips(t *testing.T) {
	payload := []byte(`{"model":"zen-1","input":"summarize this","stream":true}`)
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	defer enc.Close()

	got, err := decodeResponsesZstd(enc.EncodeAll(payload, nil))
	if err != nil {
		t.Fatalf("a legitimate %d byte body was refused: %v", len(payload), err)
	}
	if string(got) != string(payload) {
		t.Fatalf("round trip = %q, want %q", got, payload)
	}
}

// TestDecodeResponsesZstdAtLimitAccepted proves the bound is a ceiling and not
// an off-by-one that refuses the largest legal body. This is the mutation a
// `>=` would introduce.
func TestDecodeResponsesZstdAtLimitAccepted(t *testing.T) {
	got, err := decodeResponsesZstd(zstdBomb(t, int(MaxDecoded)))
	if err != nil {
		t.Fatalf("a body of EXACTLY the bound was refused (%v); the bound is off by one", err)
	}
	if int64(len(got)) != MaxDecoded {
		t.Fatalf("decoded %d bytes, want the full %d", len(got), MaxDecoded)
	}
}

// TestResponsesBodyIsZstd proves the ZAP transport's magic-number sniff, which
// is what routes a body into the bounded decoder when there is no
// Content-Encoding header to trust. A body it fails to recognize is passed to
// the JSON parser untouched, so this is the whole of that transport's guard.
func TestResponsesBodyIsZstd(t *testing.T) {
	if !responsesBodyIsZstd(zstdBomb(t, 1<<10)) {
		t.Error("a real zstd frame was not recognized, so it would skip the bounded decoder")
	}
	for name, body := range map[string][]byte{
		"json":  []byte(`{"model":"zen-1"}`),
		"short": {0x28, 0xB5},
		"empty": {},
	} {
		if responsesBodyIsZstd(body) {
			t.Errorf("%s body was taken for a zstd frame", name)
		}
	}
}
