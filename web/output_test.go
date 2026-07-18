// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	beecontext "github.com/hanzoai/beego/context"
)

// TestOutputBodySetsContentLength proves the ×24 Output.Body path: it writes
// the bytes, sets Content-Length, and defaults to 200 when no status was set
// — byte-for-byte with beego.
func TestOutputBodySetsContentLength(t *testing.T) {
	content := []byte("pong")

	rec := httptest.NewRecorder()
	ctx := webContext(httptest.NewRequest("GET", "/", nil))
	ctx.Reset(rec, ctx.Request)
	ctx.Output.Body(content)

	if rec.Body.String() != "pong" {
		t.Fatalf("body = %q, want pong", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != "4" {
		t.Fatalf("Content-Length = %q, want 4", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200", rec.Code)
	}

	// beego parity.
	brec := httptest.NewRecorder()
	bctx := beecontext.NewContext()
	bctx.Reset(brec, httptest.NewRequest("GET", "/", nil))
	bctx.Output.Body(content)
	if brec.Body.String() != rec.Body.String() ||
		brec.Header().Get("Content-Length") != rec.Header().Get("Content-Length") ||
		brec.Code != rec.Code {
		t.Fatalf("beego parity: beego(body=%q,len=%q,code=%d) web(body=%q,len=%q,code=%d)",
			brec.Body.String(), brec.Header().Get("Content-Length"), brec.Code,
			rec.Body.String(), rec.Header().Get("Content-Length"), rec.Code)
	}
}

// TestOutputSetStatusIsDeferred proves the ×4 SetStatus path: SetStatus does
// not write the header immediately; the status is applied by the next Body
// write and then cleared so a second write cannot re-emit it.
func TestOutputSetStatusIsDeferred(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := webContext(httptest.NewRequest("GET", "/", nil))
	ctx.Reset(rec, ctx.Request)

	ctx.Output.SetStatus(http.StatusCreated)
	if rec.Code != http.StatusOK {
		t.Fatalf("SetStatus must not write the header yet; Code = %d", rec.Code)
	}
	if ctx.Output.Status != http.StatusCreated {
		t.Fatalf("pending Status = %d, want 201", ctx.Output.Status)
	}

	ctx.Output.Body([]byte("x"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("Body must apply the deferred status; Code = %d, want 201", rec.Code)
	}
	if ctx.Output.Status != 0 {
		t.Fatalf("Status must be cleared after Body; got %d", ctx.Output.Status)
	}
}

// TestOutputHeader proves the ×22 Output.Header path sets a response header.
func TestOutputHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := webContext(httptest.NewRequest("GET", "/", nil))
	ctx.Reset(rec, ctx.Request)
	ctx.Output.Header("X-Trace-Id", "abc123")
	if got := rec.Header().Get("X-Trace-Id"); got != "abc123" {
		t.Fatalf("header = %q, want abc123", got)
	}
}

// TestOutputJSON proves JSON sets the content type and marshals the value;
// indentation is chosen by RunMode.
func TestOutputJSON(t *testing.T) {
	type resp struct {
		Status string `json:"status"`
	}
	rec := httptest.NewRecorder()
	ctx := webContext(httptest.NewRequest("GET", "/", nil))
	ctx.Reset(rec, ctx.Request)

	if err := ctx.Output.JSON(resp{Status: "ok"}, false, false); err != nil {
		t.Fatalf("JSON error: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var out resp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Status != "ok" {
		t.Fatalf("body did not round-trip: %v (%q)", err, rec.Body.String())
	}
	if l := rec.Header().Get("Content-Length"); l != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("Content-Length = %q, want %d", l, rec.Body.Len())
	}
}

// TestOutputJSONEscape proves the encoding flag escapes every non-ASCII rune
// to a \uXXXX sequence: the emitted body is pure ASCII yet still decodes back
// to the original multi-byte value.
func TestOutputJSONEscape(t *testing.T) {
	const original = "café"
	rec := httptest.NewRecorder()
	ctx := webContext(httptest.NewRequest("GET", "/", nil))
	ctx.Reset(rec, ctx.Request)
	ctx.Output.JSON(map[string]string{"m": original}, false, true)

	body := rec.Body.Bytes()
	for _, b := range body {
		if b >= 128 {
			t.Fatalf("escaped body has non-ASCII byte %#x: %q", b, body)
		}
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil || out["m"] != original {
		t.Fatalf("escaped body must decode back to %q: got %q (%v)", original, out["m"], err)
	}

	// escapeNonASCII is a no-op on ASCII input.
	if got := escapeNonASCII("plain-ascii_123"); got != "plain-ascii_123" {
		t.Fatalf("escapeNonASCII changed ASCII: %q", got)
	}
}
