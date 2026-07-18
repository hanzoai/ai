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
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	beecontext "github.com/hanzoai/beego/context"
)

const testMaxMemory = 1 << 26

// webContext builds a web.Context bound to a request/recorder.
func webContext(r *http.Request) *Context {
	ctx := NewContext()
	ctx.Reset(httptest.NewRecorder(), r)
	return ctx
}

// beeContext builds a beego context bound to a request/recorder.
func beeContext(r *http.Request) *beecontext.Context {
	ctx := beecontext.NewContext()
	ctx.Reset(httptest.NewRecorder(), r)
	return ctx
}

// TestCopyBodyCachesAndRewinds proves the ×162 RequestBody path: CopyBody
// returns the body bytes, caches them on RequestBody, and rewinds
// Request.Body so a later reader sees the identical bytes — byte-for-byte
// with beego's BeegoInput.CopyBody.
func TestCopyBodyCachesAndRewinds(t *testing.T) {
	const payload = `{"model":"zen","messages":[{"role":"user","content":"hi"}]}`

	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(payload))
	ctx := webContext(r)
	got := ctx.Input.CopyBody(testMaxMemory)

	if string(got) != payload {
		t.Fatalf("CopyBody returned %q, want %q", got, payload)
	}
	if string(ctx.Input.RequestBody) != payload {
		t.Fatalf("RequestBody cache = %q, want %q", ctx.Input.RequestBody, payload)
	}
	// Rewound: a downstream reader re-reads the same bytes.
	reread, _ := io.ReadAll(ctx.Request.Body)
	if string(reread) != payload {
		t.Fatalf("rewound body = %q, want %q", reread, payload)
	}

	// beego parity.
	br := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(payload))
	bctx := beeContext(br)
	bgot := bctx.Input.CopyBody(testMaxMemory)
	if !bytes.Equal(got, bgot) {
		t.Fatalf("CopyBody bytes differ from beego: web=%q beego=%q", got, bgot)
	}
	breread, _ := io.ReadAll(bctx.Request.Body)
	if !bytes.Equal(reread, breread) {
		t.Fatalf("rewound bytes differ from beego: web=%q beego=%q", reread, breread)
	}
}

// TestCopyBodyGzip proves CopyBody transparently gunzips a gzip-encoded body,
// matching beego.
func TestCopyBodyGzip(t *testing.T) {
	const payload = "the quick brown fox jumps over the lazy dog"

	gz := func() *http.Request {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write([]byte(payload))
		w.Close()
		req := httptest.NewRequest("POST", "/", &buf)
		req.Header.Set("Content-Encoding", "gzip")
		return req
	}

	got := webContext(gz()).Input.CopyBody(testMaxMemory)
	if string(got) != payload {
		t.Fatalf("gzip CopyBody = %q, want decompressed %q", got, payload)
	}
	bgot := beeContext(gz()).Input.CopyBody(testMaxMemory)
	if !bytes.Equal(got, bgot) {
		t.Fatalf("gzip bytes differ from beego: web=%q beego=%q", got, bgot)
	}
}

// TestCopyBodyNilBody proves an absent body yields empty, never a panic.
func TestCopyBodyNilBody(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Body = nil
	if got := webContext(r).Input.CopyBody(testMaxMemory); len(got) != 0 {
		t.Fatalf("nil body CopyBody = %q, want empty", got)
	}
}

// TestQueryPrefersParam proves Query returns a route parameter first, then
// falls back to the form/query value — matching beego.
func TestQueryPrefersParam(t *testing.T) {
	r := httptest.NewRequest("GET", "/?id=fromquery&only=q", nil)
	ctx := webContext(r)
	ctx.Input.SetParam("id", "fromparam")

	if v := ctx.Input.Query("id"); v != "fromparam" {
		t.Fatalf("Query(id) = %q, want route param fromparam", v)
	}
	if v := ctx.Input.Query("only"); v != "q" {
		t.Fatalf("Query(only) = %q, want form value q", v)
	}
	if v := ctx.Input.Query("absent"); v != "" {
		t.Fatalf("Query(absent) = %q, want empty", v)
	}

	// beego parity on the fallback path.
	br := httptest.NewRequest("GET", "/?id=fromquery&only=q", nil)
	bctx := beeContext(br)
	bctx.Input.SetParam("id", "fromparam")
	if bctx.Input.Query("id") != ctx.Input.Query("id") || bctx.Input.Query("only") != ctx.Input.Query("only") {
		t.Fatal("Query semantics differ from beego")
	}
}

// TestParamSetParam proves SetParam replaces an existing key and Param reads
// it back.
func TestParamSetParam(t *testing.T) {
	ctx := webContext(httptest.NewRequest("GET", "/", nil))
	ctx.Input.SetParam("k", "v1")
	ctx.Input.SetParam("k", "v2") // replace
	ctx.Input.SetParam("other", "x")
	if v := ctx.Input.Param("k"); v != "v2" {
		t.Fatalf("Param(k) = %q, want v2", v)
	}
	if v := ctx.Input.Param("missing"); v != "" {
		t.Fatalf("Param(missing) = %q, want empty", v)
	}
	if m := ctx.Input.Params(); m["k"] != "v2" || m["other"] != "x" || len(m) != 2 {
		t.Fatalf("Params() = %v, want {k:v2, other:x}", m)
	}
}

// TestHeaderURLMethodScheme covers the small readers against beego parity.
func TestHeaderURLMethodScheme(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/models/zen%2Db/access", nil)
	r.Header.Set("X-Org-Id", "acme")
	r.Header.Set("X-Forwarded-Proto", "https")
	ctx := webContext(r)
	bctx := beeContext(httptest.NewRequest("POST", "/v1/models/zen%2Db/access", nil))
	bctx.Request.Header.Set("X-Org-Id", "acme")
	bctx.Request.Header.Set("X-Forwarded-Proto", "https")

	if ctx.Input.Header("X-Org-Id") != "acme" {
		t.Fatalf("Header = %q", ctx.Input.Header("X-Org-Id"))
	}
	if ctx.Input.Method() != "POST" {
		t.Fatalf("Method = %q", ctx.Input.Method())
	}
	if ctx.Input.Scheme() != "https" {
		t.Fatalf("Scheme = %q, want https", ctx.Input.Scheme())
	}
	if ctx.Input.URL() != bctx.Input.URL() {
		t.Fatalf("URL = %q, beego = %q", ctx.Input.URL(), bctx.Input.URL())
	}
	if ctx.Input.Scheme() != bctx.Input.Scheme() {
		t.Fatalf("Scheme differs from beego: %q vs %q", ctx.Input.Scheme(), bctx.Input.Scheme())
	}
}

// TestDataMap proves the per-request data map get/set and that Data() returns
// the same backing map GetData/SetData use.
func TestDataMap(t *testing.T) {
	ctx := webContext(httptest.NewRequest("GET", "/", nil))
	ctx.Input.SetData("startTime", 42)
	if v := ctx.Input.GetData("startTime"); v != 42 {
		t.Fatalf("GetData = %v, want 42", v)
	}
	if v := ctx.Input.GetData("missing"); v != nil {
		t.Fatalf("GetData(missing) = %v, want nil", v)
	}
	if ctx.Input.Data()["startTime"] != 42 {
		t.Fatal("Data() must expose the same backing map as SetData")
	}
}

// TestSessionNilStore proves Session returns nil (never panics) when no store
// is bound — the fail-secure behavior the session-claims path relies on.
func TestSessionNilStore(t *testing.T) {
	ctx := webContext(httptest.NewRequest("GET", "/", nil))
	if ctx.Input.CruSession != nil {
		t.Fatal("fresh input must have no session store")
	}
	if v := ctx.Input.Session("user"); v != nil {
		t.Fatalf("Session with nil store = %v, want nil", v)
	}
}
