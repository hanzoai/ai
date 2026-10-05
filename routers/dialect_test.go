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

// A refusal written before the handler runs leaves /v1/decisions in the service's
// words, with both retry headers and a request id; any other path is untouched.
func TestDialectWordsEveryRefusal(t *testing.T) {
	p := ask(http.MethodPost, "/v1/decisions").through(Dialect, limited)
	if p.status() != 429 || p.said() != `{"error":{"message":"Rate limit exceeded. Retry after 3 seconds.","type":"rate_limit_error","code":429}}` {
		t.Fatalf("decisions 429 => %d %s", p.status(), p.said())
	}
	if p.replied("Retry-After") != "3" || p.replied("Retry-After-Ms") != "3000" || p.replied("X-Request-Id") == "" {
		t.Fatalf("decisions 429 headers: Retry-After=%q ms=%q id=%q", p.replied("Retry-After"), p.replied("Retry-After-Ms"), p.replied("X-Request-Id"))
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

// The published document says what /v1/decisions takes and answers: the fields a
// body must carry, each question's kinds and bounds, and every refusal with the
// waits it asks for.
func TestTheDecisionPathIsPublished(t *testing.T) {
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

	op := paths["/v1/decisions"].(map[string]any)["post"].(map[string]any)
	if tags := op["tags"].([]any); !slices.Equal(tags, []any{"decisions"}) {
		t.Fatalf("decisions tags = %v, want [decisions]", tags)
	}

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

	if r := required("ai.DecisionsRequest"); !slices.Equal(r, []any{"model"}) {
		t.Errorf("decisions requires %v (state and questions give way to a handle)", r)
	}
	if q := prop("ai.DecisionsRequest", "questions"); q["minProperties"] != 1 || q["maxProperties"] != 100 {
		t.Errorf("questions bounds = %v", q)
	}
	for _, f := range []string{"observe", "handle"} {
		if _, ok := schema("ai.DecisionsRequest")["properties"].(map[string]any)[f]; !ok {
			t.Errorf("decisions request has no %s", f)
		}
	}
	// A value the wire takes in more than one form is an OPEN schema that says the
	// forms in its description. An anyOf there made every generator wrap it in a
	// type of its own: Python's wrapper refused a plain string state and read a
	// legend's strings back as wrappers.
	open := func(where string, s map[string]any) {
		for _, k := range []string{"anyOf", "oneOf", "type", "$ref"} {
			if _, ok := s[k]; ok {
				t.Errorf("%s = %v, want an open schema", where, s)
				return
			}
		}
		if d, _ := s["description"].(string); d == "" {
			t.Errorf("%s does not say the forms it takes", where)
		}
	}
	open("state", prop("ai.DecisionsRequest", "state"))
	open("boolean instructions", prop("ai.DecisionsBoolean", "instructions"))
	open("noul instructions", prop("ai.DecisionsNoul", "instructions"))
	open("choice instructions", prop("ai.DecisionsChoice", "instructions"))
	open("choice criteria", prop("ai.DecisionsChoice", "criteria"))
	open("noul side", prop("ai.DecisionSides", "true"))
	open("score level", prop("ai.DecisionsScore", "criteria")["items"].(map[string]any))
	open("legend entry", prop("ai.DecisionsAnswer", "legend")["additionalProperties"].(map[string]any))
	for _, yes := range []string{"ai.DecisionsBoolean", "ai.DecisionsNoul"} {
		if slices.Contains(required(yes), "instructions") {
			t.Errorf("%s instructions are required, want optional", yes)
		}
	}
	if e := prop("ai.DecisionsChoice", "type")["enum"]; !slices.Equal(e.([]any), []any{"choice"}) {
		t.Errorf("choice type enum = %v", e)
	}

	bounds := []struct {
		schema, key string
		min, max    any
	}{
		{"ai.DecisionsScore", "minItems", 1, nil},
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
	if len(q["oneOf"].([]any)) != 4 || d["propertyName"] != "type" || m["score"] != "#/components/schemas/ai.DecisionsScore" ||
		m["boolean"] != "#/components/schemas/ai.DecisionsBoolean" || m["noul"] != "#/components/schemas/ai.DecisionsNoul" {
		t.Errorf("a question is not a boolean (or noul, its other spelling), a choice or a score by type: %v", q)
	}
	if e := prop("ai.DecisionsAnswer", "type")["enum"]; !slices.Equal(e.([]any), []any{"boolean", "noul", "choice", "score"}) {
		t.Errorf("answer type enum = %v", e)
	}
	if p := prop("ai.DecisionsAnswer", "probability"); p["type"] != "number" {
		t.Errorf("a boolean answer's probability = %v", p)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(`{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul"}}}`))
	req.Header.Set("Authorization", "Bearer sk-routers-jev")
	resp, err := built().Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "jev-latest") {
		t.Errorf("jev-latest => %d %s", resp.StatusCode, b)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the decision service saw %d call(s) for a Jev name", n)
	}
}

// Every filter the service runs, in the order it runs them: a panic on
// /v1/decisions still leaves in the service's shape with a request id, and so does a
// refusal on the path spelled with a trailing slash or in capitals.
func TestDialectCoversPanicsAndEverySpelling(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	InstallFilters(app)
	app.Raw(http.MethodPost, "/v1/decisions", func(c *zip.Ctx) error { panic("boom: secret") })
	req := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"}))
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || string(b) != `{"error":{"code":500,"message":"internal error"}}` || resp.Header.Get("X-Request-Id") == "" {
		t.Fatalf("panic => %d %s X-Request-Id=%q", resp.StatusCode, b, resp.Header.Get("X-Request-Id"))
	}

	for _, p := range []string{"/v1/decisions/", "/V1/DECISIONS"} {
		pr := ask(http.MethodPost, p).through(Dialect, enveloped)
		if pr.said() != `{"error":{"code":401,"message":"invalid API key"}}` || pr.replied("X-Request-Id") == "" {
			t.Errorf("%s => %s id=%q", p, pr.said(), pr.replied("X-Request-Id"))
		}
		if pr = ask(http.MethodPost, p).through(Dialect, limited); pr.replied("Retry-After-Ms") != "3000" {
			t.Errorf("%s 429 Retry-After-Ms=%q", p, pr.replied("Retry-After-Ms"))
		}
	}
}

// A layer that RETURNS its refusal — the framework reading a body over the socket's
// limit, a typed error, or an error that chose nothing — is answered in the
// service's words with a request id, never by the framework's own renderer.
func TestDialectWordsReturnedRefusals(t *testing.T) {
	cases := []struct {
		err  error
		code int
		body string
	}{
		{fasthttp.ErrBodyTooLarge, 422, `{"error":{"code":"request_too_long","message":"the request body is over the limit of 16777216 bytes"}}`},
		{fiber.ErrRequestEntityTooLarge, 422, `{"error":{"code":"request_too_long","message":"the request body is over the limit of 16777216 bytes"}}`},
		{zip.ErrBadRequest("unreadable body"), 400, `{"error":{"code":400,"message":"unreadable body"}}`},
		{errors.New("dial tcp 10.0.0.7:8080: refused"), 500, `{"error":{"code":500,"message":"internal error"}}`},
	}
	for _, tc := range cases {
		err := tc.err
		p := ask(http.MethodPost, "/v1/decisions").through(Dialect, func(*zip.Ctx) error { return err })
		if p.status() != tc.code || p.said() != tc.body || p.replied("X-Request-Id") == "" {
			t.Errorf("%v => %d %s id=%q", tc.err, p.status(), p.said(), p.replied("X-Request-Id"))
		}
	}
}
