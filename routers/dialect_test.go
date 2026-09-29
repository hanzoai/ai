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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// limited is a filter that refuses the way RateLimitFilter does.
func limited(c *zip.Ctx) error {
	c.SetHeader("Retry-After", "3")
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(http.StatusTooManyRequests, []byte(`{"error":{"message":"Rate limit exceeded. Retry after 3 seconds.","type":"rate_limit_error","code":429}}`))
}

// enveloped is a filter that refuses in the house envelope, as the auth filters do.
func enveloped(c *zip.Ctx) error {
	return c.JSON(http.StatusUnauthorized, map[string]string{"status": "error", "msg": "invalid API key"})
}

// A refusal written before the handler runs leaves a decision path in that path's
// words, with both retry headers and a request id; any other path is untouched.
func TestDialectWordsEveryRefusal(t *testing.T) {
	p := ask(http.MethodPost, "/v1/systemone").through(Dialect, limited)
	if p.status() != 429 || p.said() != `{"detail":"Rate limit exceeded. Retry after 3 seconds."}` {
		t.Fatalf("systemone 429 => %d %s", p.status(), p.said())
	}
	if p.replied("Retry-After") != "3" || p.replied("Retry-After-Ms") != "3000" || p.replied("X-Request-Id") == "" {
		t.Fatalf("systemone 429 headers: Retry-After=%q ms=%q id=%q", p.replied("Retry-After"), p.replied("Retry-After-Ms"), p.replied("X-Request-Id"))
	}

	p = ask(http.MethodPost, "/v1/decisions").with("X-Request-Id", "req-7").through(Dialect, enveloped)
	if p.status() != 401 || p.said() != `{"error":{"code":401,"message":"invalid API key"}}` || p.replied("X-Request-Id") != "req-7" {
		t.Fatalf("decisions 401 => %d %s id=%q", p.status(), p.said(), p.replied("X-Request-Id"))
	}

	p = ask(http.MethodPost, "/v1/chat/completions").through(Dialect, limited)
	if p.said() != `{"error":{"message":"Rate limit exceeded. Retry after 3 seconds.","type":"rate_limit_error","code":429}}` || p.replied("Retry-After-Ms") != "" {
		t.Fatalf("a path that is not a decision path was reworded: %s ms=%q", p.said(), p.replied("Retry-After-Ms"))
	}
}

// The published document says what the two decision paths take and answer: the
// fields a body must carry, each question's kinds and bounds, every refusal with
// the waits it asks for, and that /v1/systemone is compat, out of the contract.
func TestTheDecisionPathsArePublished(t *testing.T) {
	doc := Document(built())
	paths := doc["paths"].(map[string]any)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	schema := func(name string) map[string]any {
		s, ok := schemas[name].(map[string]any)
		if !ok {
			t.Fatalf("no schema %s", name)
		}
		return s
	}
	prop := func(name, field string) map[string]any {
		return schema(name)["properties"].(map[string]any)[field].(map[string]any)
	}
	required := func(name string) []any { r, _ := schema(name)["required"].([]any); return r }

	one := paths["/v1/systemone"].(map[string]any)["post"].(map[string]any)
	if tags := one["tags"].([]any); !slices.Equal(tags, []any{"systemone", "compat"}) {
		t.Fatalf("systemone tags = %v, want [systemone compat]", tags)
	}
	native := paths["/v1/decisions"].(map[string]any)["post"].(map[string]any)
	if tags := native["tags"].([]any); !slices.Equal(tags, []any{"decisions"}) {
		t.Fatalf("decisions tags = %v, want [decisions]", tags)
	}

	for _, op := range []map[string]any{native, one} {
		responses := op["responses"].(map[string]any)
		for _, code := range []string{"200", "400", "401", "402", "403", "422", "429", "502", "503", "529"} {
			r, ok := responses[code].(map[string]any)
			if !ok {
				t.Fatalf("%s has no %s", op["operationId"], code)
			}
			h, _ := r["headers"].(map[string]any)
			if _, ok := h["X-Request-Id"]; !ok {
				t.Errorf("%s %s carries no X-Request-Id", op["operationId"], code)
			}
			_, wait := h["Retry-After-Ms"]
			if want := code == "402" || code == "429" || code == "529"; wait != want || (want && h["Retry-After"] == nil) {
				t.Errorf("%s %s retry headers = %v", op["operationId"], code, h)
			}
		}
	}

	if r := required("ai.SystemoneRequest"); !slices.Equal(r, []any{"model", "state", "questions"}) {
		t.Errorf("systemone requires %v", r)
	}
	if r := required("ai.DecisionsRequest"); !slices.Equal(r, []any{"model"}) {
		t.Errorf("decisions requires %v (state and questions give way to a handle)", r)
	}
	for _, name := range []string{"ai.DecisionsRequest", "ai.SystemoneRequest"} {
		q := prop(name, "questions")
		if q["minProperties"] != 1 || q["maxProperties"] != 100 {
			t.Errorf("%s questions bounds = %v", name, q)
		}
	}
	for _, f := range []string{"observe", "handle"} {
		if _, ok := schema("ai.DecisionsRequest")["properties"].(map[string]any)[f]; !ok {
			t.Errorf("decisions request has no %s", f)
		}
	}
	if _, ok := prop("ai.DecisionsNoul", "instructions")["anyOf"]; !ok || slices.Contains(required("ai.DecisionsNoul"), "instructions") {
		t.Errorf("instructions is not optional Content: %v", prop("ai.DecisionsNoul", "instructions"))
	}
	if e := prop("ai.DecisionsChoice", "type")["enum"]; !slices.Equal(e.([]any), []any{"choice"}) {
		t.Errorf("choice type enum = %v", e)
	}

	bounds := []struct {
		schema, key string
		min, max    any
	}{
		{"ai.DecisionsChoice", "minProperties", 2, nil},
		{"ai.DecisionsScore", "minItems", 1, nil},
		{"ai.SystemoneChoice", "minProperties", 2, 255},
		{"ai.SystemoneScore", "minItems", 1, 10},
	}
	for _, b := range bounds {
		c := prop(b.schema, "criteria")
		maxKey := "maxProperties"
		if b.key == "minItems" {
			maxKey = "maxItems"
		}
		if c[b.key] != b.min || c[maxKey] != b.max {
			t.Errorf("%s criteria = %v, want %s=%v %s=%v", b.schema, c, b.key, b.min, maxKey, b.max)
		}
	}

	q := schema("ai.DecisionsQuestion")
	d, _ := q["discriminator"].(map[string]any)
	m, _ := d["mapping"].(map[string]any)
	if len(q["oneOf"].([]any)) != 3 || d["propertyName"] != "type" || m["score"] != "#/components/schemas/ai.DecisionsScore" {
		t.Errorf("a question is not one of three kinds by type: %v", q)
	}
	if r := required("ai.SystemoneResponse"); !slices.Equal(r, []any{"model", "answers", "usage"}) {
		t.Errorf("systemone answer requires %v", r)
	}
	if r := required("ai.DecisionsUsage"); !slices.Equal(r, []any{"input_tokens", "output_tokens"}) {
		t.Errorf("usage requires %v", r)
	}
}

// A Jev-named alias of Kai is no alias: through the whole router — filters, the
// alias rewrite and the handler — a caller asking for jev-latest is told it is an
// unknown model, and nothing reaches the decision service.
func TestJevAliasIsRefusedThroughTheRouter(t *testing.T) {
	restore, err := object.UseMemoryDB(fmt.Sprintf("file:routers_jev_%d?mode=memory&cache=shared", time.Now().UnixNano()), &object.Provider{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	if _, err := object.AddProvider(&object.Provider{
		Owner: "acme", Name: "acme-openai", Category: "Model", Type: "OpenAI",
		ProviderKey: "sk-routers-jev", ProviderUrl: "http://127.0.0.1:1/v1", ClientSecret: "x", State: "Active",
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(svc.Close)
	t.Setenv("KAI_URL", svc.URL)
	shipped, err := os.ReadFile("../conf/models.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/models.yaml"
	if err := os.WriteFile(path, append(shipped, []byte("  jev-latest:\n    alias_of: kai\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := controllers.InitModelConfig(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controllers.InitModelConfig("../conf/models.yaml") })

	app := built()
	for _, p := range []string{"/v1/systemone", "/v1/decisions"} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul"}}}`))
		req.Header.Set("Authorization", "Bearer sk-routers-jev")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "jev-latest") {
			t.Errorf("%s jev-latest => %d %s", p, resp.StatusCode, b)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the decision service saw %d call(s) for a Jev name", n)
	}
}

// Every filter the service runs, in the order it runs them: a panic on a decision
// path still leaves in that path's shape with a request id, and so does a refusal
// on the path spelled with a trailing slash or in capitals.
func TestDialectCoversPanicsAndEverySpelling(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	InstallFilters(app)
	app.Raw(http.MethodPost, "/v1/systemone", func(c *zip.Ctx) error { panic("boom: secret") })
	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{}`))
	req.Header.Set("Authorization", authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"}))
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || string(b) != `{"detail":"internal error"}` || resp.Header.Get("X-Request-Id") == "" {
		t.Fatalf("panic => %d %s X-Request-Id=%q", resp.StatusCode, b, resp.Header.Get("X-Request-Id"))
	}

	for _, p := range []string{"/v1/systemone/", "/V1/SYSTEMONE"} {
		pr := ask(http.MethodPost, p).through(Dialect, limited)
		if pr.said() != `{"detail":"Rate limit exceeded. Retry after 3 seconds."}` || pr.replied("X-Request-Id") == "" || pr.replied("Retry-After-Ms") != "3000" {
			t.Errorf("%s => %s id=%q ms=%q", p, pr.said(), pr.replied("X-Request-Id"), pr.replied("Retry-After-Ms"))
		}
	}
}

// A layer that RETURNS its refusal — the framework reading a body over the socket's
// limit, a typed error, or an error that chose nothing — is answered in the path's
// words with a request id, never by the framework's own renderer.
func TestDialectWordsReturnedRefusals(t *testing.T) {
	cases := []struct {
		path string
		err  error
		code int
		body string
	}{
		{"/v1/systemone", fasthttp.ErrBodyTooLarge, 422, `{"detail":[{"loc":["body"],"msg":"the request body is over the limit of 16777216 bytes","type":"request_too_long"}]}`},
		{"/v1/decisions", fiber.ErrRequestEntityTooLarge, 422, `{"error":{"code":"request_too_long","message":"the request body is over the limit of 16777216 bytes"}}`},
		{"/v1/decisions", zip.ErrBadRequest("unreadable body"), 400, `{"error":{"code":400,"message":"unreadable body"}}`},
		{"/v1/systemone", errors.New("dial tcp 10.0.0.7:8080: refused"), 500, `{"detail":"internal error"}`},
	}
	for _, tc := range cases {
		err := tc.err
		p := ask(http.MethodPost, tc.path).through(Dialect, func(*zip.Ctx) error { return err })
		if p.status() != tc.code || p.said() != tc.body || p.replied("X-Request-Id") == "" {
			t.Errorf("%s %v => %d %s id=%q", tc.path, tc.err, p.status(), p.said(), p.replied("X-Request-Id"))
		}
	}
}
