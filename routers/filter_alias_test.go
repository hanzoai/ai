// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package routers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
)

// A request naming an alias reaches everything after the filter naming the id the
// alias stands for, every other field as sent, on every path that names a model but
// /v1/decisions, which resolves an alias after the credential. Any other id passes
// byte for byte.
func TestAnAliasIsNamedAsTheIDItStandsFor(t *testing.T) {
	prev := canonical
	canonical = func(model string) (string, bool) {
		if strings.EqualFold(model, "hanzoai/enso") {
			return "hanzo/enso", true
		}
		return "", false
	}
	t.Cleanup(func() { canonical = prev })

	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		p := ask(http.MethodPost, path).
			body([]byte(`{"model":"HanzoAI/Enso","stream":true,"messages":[{"role":"user","content":"hi"}]}`)).
			through(AliasFilter)
		var got struct {
			Model    string          `json:"model"`
			Stream   bool            `json:"stream"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal([]byte(p.handed()), &got); err != nil || p.status() != http.StatusOK {
			t.Fatalf("%s: status %d, handed %q", path, p.status(), p.handed())
		}
		if got.Model != "hanzo/enso" || !got.Stream || string(got.Messages) != `[{"role":"user","content":"hi"}]` {
			t.Fatalf("%s: handed %s, want hanzo/enso and every other field as sent", path, p.handed())
		}
	}

	for _, path := range []string{"/v1/decisions", "/V1/Decisions/"} {
		sent := `{"model":"HanzoAI/Enso","state":"x","questions":{"q":{"type":"noul"}}}`
		if p := ask(http.MethodPost, path).body([]byte(sent)).through(AliasFilter); p.handed() != sent {
			t.Fatalf("%s: %s was rewritten to %s", path, sent, p.handed())
		}
	}

	for _, sent := range []string{
		`{"model":"hanzo/enso","messages":[]}`,
		`{"model":"enso-auto","messages":[]}`,
		`{"messages":[]}`,
		`not json`,
	} {
		if p := ask(http.MethodPost, "/v1/chat/completions").body([]byte(sent)).through(AliasFilter); p.handed() != sent {
			t.Fatalf("%s was rewritten to %s", sent, p.handed())
		}
	}
}

// Through the whole router, a decision body behind a credential nobody issued is
// refused 401 in the service's words without being parsed or decoded: the alias rewrite
// never reads it, and 64 MiB of gzip costs what its wire bytes cost.
func TestADecisionBodyIsNotReadBeforeItsCredential(t *testing.T) {
	var seen atomic.Int32
	prev := canonical
	canonical = func(m string) (string, bool) { seen.Add(1); return prev(m) }
	t.Cleanup(func() { canonical = prev })
	restore, err := object.UseMemoryDB(fmt.Sprintf("file:routers_preauth_%d?mode=memory&cache=shared", time.Now().UnixNano()), &object.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10, BodyLimit: controllers.MaxTranscribeUpload + 1<<20})
	Register(app)

	var zb bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&zb, gzip.BestCompression)
	_, _ = zw.Write([]byte(`{"state":1,` + strings.Repeat(" ", 64<<20) + `"model":"kai"}`))
	_ = zw.Close()
	for _, gz := range []bool{false, true} {
		body := []byte(`{"model":"kai","state":"x","questions":{"q":{"type":"noul"}}}`)
		if gz {
			body = zb.Bytes()
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/decisions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-nobody-issued-this-000000000000")
		if gz {
			req.Header.Set("Content-Encoding", "gzip")
		}
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		runtime.ReadMemStats(&b)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("X-Request-Id") == "" {
			t.Fatalf("gzip=%v unauthenticated => %d %s", gz, resp.StatusCode, out)
		}
		if alloc := b.TotalAlloc - a.TotalAlloc; gz && alloc > 8<<20 {
			t.Fatalf("refusing %d KiB of unauthenticated gzip allocated %d MiB: it was decoded", zb.Len()>>10, alloc>>20)
		}
	}
	if n := seen.Load(); n != 0 {
		t.Fatalf("the alias rewrite parsed %d decision body(ies) before the credential", n)
	}
}

// The filters ahead of the credential leave a body with a Content-Encoding as sent:
// neither the alias rewrite nor the auto route reads its model, so 20 MiB of gzip
// reaches the handler coded, having cost what its wire bytes cost, and the handler
// reads it once it has authenticated the sender. A plain body naming an alias is
// rewritten as before, and "identity" is plain.
func TestACodedBodyIsNotDecodedAheadOfTheCredential(t *testing.T) {
	var seen, routed atomic.Int32
	prev, prevRoute := canonical, routeAuto
	canonical = func(m string) (string, bool) {
		seen.Add(1)
		if strings.EqualFold(m, "hanzoai/enso") {
			return "hanzo/enso", true
		}
		return "", false
	}
	routeAuto = func(*zip.Ctx) { routed.Add(1) }
	t.Cleanup(func() { canonical, routeAuto = prev, prevRoute })

	var coding, arrived string
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10, BodyLimit: controllers.MaxTranscribeUpload + 1<<20})
	app.Use(zip.H(AliasFilter))
	app.Use(zip.H(AutoRouteFilter))
	app.Raw(zip.MethodAll, "/*", func(c *zip.Ctx) error {
		coding, arrived = c.Header("Content-Encoding"), string(c.Fiber().Request().Body())
		return nil
	})
	send := func(body []byte, enc string) uint64 {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if enc != "" {
			req.Header.Set("Content-Encoding", enc)
		}
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		runtime.ReadMemStats(&b)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("Content-Encoding %q => %v %v", enc, resp, err)
		}
		return b.TotalAlloc - a.TotalAlloc
	}

	var zb bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&zb, gzip.BestCompression)
	_, _ = zw.Write([]byte(`{"model":"hanzoai/enso",` + strings.Repeat(" ", 20<<20) + `"messages":[]}`))
	_ = zw.Close()
	if alloc := send(zb.Bytes(), "gzip"); alloc > 4<<20 {
		t.Fatalf("passing %d KiB of gzip on allocated %d MiB: it was decoded", zb.Len()>>10, alloc>>20)
	}
	if coding != "gzip" || arrived != zb.String() || seen.Load() != 0 || routed.Load() != 0 {
		t.Fatalf("a gzip body reached the handler as %q, changed %v; the alias table was read %d time(s), the auto route %d",
			coding, arrived != zb.String(), seen.Load(), routed.Load())
	}

	for _, enc := range []string{"", "identity"} {
		send([]byte(`{"model":"HanzoAI/Enso","messages":[]}`), enc)
		if !strings.Contains(arrived, `"model":"hanzo/enso"`) {
			t.Fatalf("Content-Encoding %q: a plain alias reached the handler as %s", enc, arrived)
		}
	}
	if seen.Load() != 2 || routed.Load() != 2 {
		t.Fatalf("plain bodies: the alias table was read %d time(s), the auto route %d; want 2 each", seen.Load(), routed.Load())
	}
}
