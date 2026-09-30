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

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/decimal"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/proxy"
)

// The route these tests call: the caller names the SKU, the upstream is sent the id
// the route table maps it to.
const (
	nativeSKU = "claude-opus-4"
	nativeID  = "anthropic-claude-opus-4"
	nativeKey = "upstream-credential"
)

// maximal is a Messages request carrying every field the shared account serves,
// written the way no encoder writes JSON: uneven whitespace, unsorted keys, escapes,
// and number spellings a re-marshal would change. Everything in it must reach the
// upstream as written.
const maximal = `{
  "model": "claude-opus-4",
  "max_tokens":   2048,
  "system": [
    {"type": "text", "text": "You are terse.", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
  ],
  "messages": [
    {"role": "user", "content": [
      {"type": "text", "text": "café 😀 <\/script>"},
      {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}},
      {"type": "document", "source": {"type": "url", "url": "https://example.com/a.pdf"}, "citations": {"enabled": true}}
    ]},
    {"role": "assistant", "content": [
      {"type": "thinking", "thinking": "", "signature": "EqQBCkYIBxgCKkA="},
      {"type": "tool_use", "id": "toolu_01", "name": "get_weather", "input": {"city": "SF"}}
    ]},
    {"role": "user", "content": [
      {"type": "tool_result", "tool_use_id": "toolu_01", "content": "18C", "cache_control": {"type": "ephemeral"}}
    ]}
  ],
  "tools": [
    {"name": "get_weather", "description": "Weather by city.", "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"], "additionalProperties": false}, "strict": true, "eager_input_streaming": true, "cache_control": {"type": "ephemeral"}},
    {"type": "web_fetch_20260209", "name": "web_fetch", "max_uses": 2},
    {"type": "bash_20250124", "name": "bash"}
  ],
  "tool_choice": {"type": "auto", "disable_parallel_tool_use": true},
  "thinking": {"type": "adaptive", "display": "summarized"},
  "output_config": {"effort": "high", "format": {"type": "json_schema", "schema": {"type": "object"}}},
  "temperature": 0,
  "top_p": 0.950,
  "top_k": 40,
  "stop_sequences": ["\n\nHuman:", "STOP"],
  "metadata": {"user_id": "u-9f2c"},
  "service_tier": "standard_only",
  "context_management": {"edits": [{"type": "clear_tool_uses_20250919"}]},
  "cache_control": {"type": "ephemeral"},
  "speed": "standard",
  "inference_geo": "global",
  "stream": false
}`

// everything is maximal plus what only the caller's own connected account serves:
// a held file, a container, a remote MCP server, cache diagnostics, a faster lane,
// a paid server tool, and a field not published yet.
var everything = strings.NewReplacer(
	`"stream": false`, `"stream": false,
  "mcp_servers": [{"type": "url", "url": "https://mcp.example.com/sse", "name": "ex"}],
  "container": "container_011",
  "diagnostics": {"previous_message_id": null},
  "x_unreleased": {"n": 1.50e+2, "big": 12345678901234567890}`,
	`"speed": "standard"`, `"speed": "fast"`,
	`{"type": "bash_20250124", "name": "bash"}`, `{"type": "web_search_20260209", "name": "web_search"}, {"type": "mcp_toolset", "mcp_server_name": "ex"}`,
	`"source": {"type": "url", "url": "https://example.com/a.pdf"}`, `"source": {"type": "file", "file_id": "file_011"}`,
).Replace(maximal)

// addressed is req as the upstream must receive it: the same bytes, with the one
// field that names the model naming the upstream id instead.
func addressed(t *testing.T, req string) string {
	t.Helper()
	from, to := `"model": "`+nativeSKU+`"`, `"model": "`+nativeID+`"`
	if n := strings.Count(req, from); n != 1 {
		t.Fatalf("fixture names the model %d times", n)
	}
	return strings.Replace(req, from, to, 1)
}

// received is what a fake native upstream was sent.
type received struct {
	mu     sync.Mutex
	calls  int
	path   string
	body   string
	header http.Header
}

func (r *received) get() (calls int, path, body string, header http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.path, r.body, r.header
}

// nativeUpstream records the exact body and headers of each request it is sent and
// answers every one with reply.
func nativeUpstream(t *testing.T, contentType, reply string) (string, *received) {
	t.Helper()
	got := &received{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.calls++
		got.path, got.body, got.header = r.URL.Path, string(b), r.Header.Clone()
		got.mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, got
}

// nativeReply is one whole native response reporting both cache counts.
const nativeReply = `{"id":"msg_01","type":"message","role":"assistant","model":"` + nativeID + `",` +
	`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,` +
	`"usage":{"input_tokens":1200,"cache_creation_input_tokens":300,"cache_read_input_tokens":5000,"output_tokens":340}}`

// nativeStream is the same reply streamed, ping included.
const nativeStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","content":[],"model":"` + nativeID + `","stop_reason":null,"usage":{"input_tokens":1200,"cache_creation_input_tokens":300,"cache_read_input_tokens":5000,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: ping\n" +
	`data: {"type": "ping"}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":340}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// nativeCost is what either reply must cost: the SKU's price, cache included.
func nativeCost() int64 { return calculateCostCentsWithCache(nativeSKU, 1200, 340, 5000, 300) }

// payer is a signed-in member of acme with a funded ledger, calling a route whose
// provider is a native upstream owned by owner: "admin" is the shared account,
// "acme" the payer's own connected one.
type payer struct {
	credential string
	subject    string
	funded     int64
	usage      func() []object.UsageEvent
}

func nativePayer(t *testing.T, owner, url string) payer {
	t.Helper()
	withStore(t)
	people := withIAM(t)
	proxy.InitHttpClient()

	available := new(int64)
	*available = 100000
	t.Setenv("commerceEndpoint", fakeCommerceBalance(t, available))
	t.Setenv("commerceToken", "test-svc-token")

	if _, err := object.AddProvider(&object.Provider{
		Owner: owner, Name: "do-ai", Category: "Model", Type: "Anthropic",
		SubType: nativeSKU, ProviderUrl: url, ClientSecret: nativeKey, State: "Active",
	}); err != nil {
		t.Fatal(err)
	}
	if r := resolveModelRoute(nativeSKU); r == nil || r.upstreamModel != nativeID || r.providerName != "do-ai" {
		t.Fatalf("route %s no longer maps to do-ai/%s: %+v", nativeSKU, nativeID, r)
	}

	user := &iam.User{Owner: "acme", Name: "val"}
	subject := user.PayerSubject("acme")
	object.GlobalBalanceLedger.SetBalance(subject, *available)

	var mu sync.Mutex
	var seen []object.UsageEvent
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, u)
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	return payer{
		credential: people.signedIn(t, user),
		subject:    subject,
		funded:     *available,
		usage: func() []object.UsageEvent {
			mu.Lock()
			defer mu.Unlock()
			return append([]object.UsageEvent(nil), seen...)
		},
	}
}

// post sends body to /v1/messages as a client would, with a pinned API version, two
// anthropic-beta lines and a header of its own, and returns what it received.
func (p payer) post(t *testing.T, body string) (*ApiController, string) {
	t.Helper()
	c := as(visit("POST", "/v1/messages"), p.credential)
	h := &c.Fiber().Request().Header
	h.Set("anthropic-version", "2023-01-01")
	h.Add("anthropic-beta", "context-management-2025-06-27,interleaved-thinking-2025-05-14")
	h.Add("anthropic-beta", "fine-grained-tool-streaming-2025-05-14")
	h.Set("X-Client-Only", "stays-here")
	c.Fiber().Request().SetBody([]byte(body))
	c.AnthropicMessages()
	return c, sent(c)
}

// billed checks the call was priced as the SKU with both cache counts in, and filed
// under the SKU rather than the upstream id.
func (p payer) billed(t *testing.T) {
	t.Helper()
	balance, reserved, _, _ := object.GlobalBalanceLedger.Snapshot(p.subject)
	if reserved != 0 {
		t.Errorf("%d cents are still reserved after the answer", reserved)
	}
	if spent, want := p.funded-balance, nativeCost(); spent != want {
		t.Errorf("the ledger took %d cents; the SKU prices this answer, cache included, at %d", spent, want)
	}
	ev := p.usage()
	if len(ev) != 1 {
		t.Fatalf("%d usage events, want 1: %+v", len(ev), ev)
	}
	if ev[0].Model != nativeSKU {
		t.Errorf("usage filed under %q, want the SKU %q", ev[0].Model, nativeSKU)
	}
}

// headersArrived checks the payer's dialect crossed and nothing else of theirs did.
func headersArrived(t *testing.T, h http.Header) {
	t.Helper()
	if v := h.Get("Anthropic-Version"); v != "2023-01-01" {
		t.Errorf("anthropic-version = %q, want the caller's 2023-01-01", v)
	}
	want := []string{"context-management-2025-06-27,interleaved-thinking-2025-05-14", "fine-grained-tool-streaming-2025-05-14"}
	if got := h.Values("Anthropic-Beta"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("anthropic-beta = %q, want %q", got, want)
	}
	if v := h.Get("X-Api-Key"); v != nativeKey {
		t.Errorf("x-api-key = %q, want the provider's credential", v)
	}
	if v := h.Get("Authorization"); v != "" {
		t.Errorf("the caller's credential crossed to the upstream: %q", v)
	}
	if v := h.Get("X-Client-Only"); v != "" {
		t.Errorf("an arbitrary client header crossed to the upstream: %q", v)
	}
}

// EVERY BYTE THE CALLER SENT REACHES THE UPSTREAM, AND ONLY THE MODEL CHANGES.
//
// The relay used to re-marshal a struct with nine fields, so metadata, top_p, top_k,
// stop_sequences, output_config, a tool's cache_control, and every field published
// after the struct was written were dropped; temperature 0 went too, being the zero
// value of a field marked omitempty. A re-marshal also reorders and respaces, and a
// prompt-cache prefix is computed over bytes.
func TestNativeUpstreamReceivesTheCallersBytes(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, globalProviderOwner, url)

	c, body := p.post(t, maximal)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), body)
	}
	calls, path, sentBody, header := got.get()
	if calls != 1 || path != "/v1/messages" {
		t.Fatalf("upstream called %d times at %q", calls, path)
	}
	if want := addressed(t, maximal); sentBody != want {
		t.Errorf("the upstream was not sent the caller's bytes.\n got: %s\nwant: %s", sentBody, want)
	}
	headersArrived(t, header)
	if body != nativeReply {
		t.Errorf("the client was not handed the upstream's answer.\n got: %s\nwant: %s", body, nativeReply)
	}
	p.billed(t)
}

// A tool's cache_control is its own breakpoint; without it everything the tool list
// prefixes is read again at full price on every turn.
func TestNativeUpstreamKeepsToolCacheControl(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, globalProviderOwner, url)
	p.post(t, maximal)

	_, _, sentBody, _ := got.get()
	var req struct {
		Model string `json:"model"`
		Tools []struct {
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(sentBody), &req); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if len(req.Tools) == 0 || req.Tools[0].CacheControl == nil || req.Tools[0].CacheControl.Type != "ephemeral" {
		t.Errorf("the tool's cache_control did not arrive: %s", sentBody)
	}
	if req.Model != nativeID {
		t.Errorf("model = %q, want the route's upstream id %q", req.Model, nativeID)
	}
}

// A streamed answer reaches the client as the upstream sent it, and is billed from
// the usage it carried, at the SKU's price.
func TestNativeStreamPassesThroughAndBillsTheSKU(t *testing.T) {
	url, got := nativeUpstream(t, "text/event-stream", nativeStream)
	p := nativePayer(t, globalProviderOwner, url)

	req := strings.Replace(maximal, `"stream": false`, `"stream": true`, 1)
	c, body := p.post(t, req)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), body)
	}
	if body != nativeStream {
		t.Errorf("the stream was changed on the way through.\n got: %q\nwant: %q", body, nativeStream)
	}
	_, _, sentBody, header := got.get()
	if want := addressed(t, req); sentBody != want {
		t.Errorf("the upstream was not sent the caller's bytes.\n got: %s\nwant: %s", sentBody, want)
	}
	headersArrived(t, header)
	p.billed(t)
}

// With no anthropic-version the upstream is asked for the default one rather than
// sent a request it refuses, and no anthropic-beta is invented.
func TestNativeUpstreamGetsADefaultVersion(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, globalProviderOwner, url)

	c := as(visit("POST", "/v1/messages"), p.credential)
	c.Fiber().Request().SetBody([]byte(maximal))
	c.AnthropicMessages()

	_, _, _, header := got.get()
	if v := header.Get("Anthropic-Version"); v != anthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", v, anthropicVersion)
	}
	if v := header.Values("Anthropic-Beta"); len(v) != 0 {
		t.Errorf("anthropic-beta invented: %q", v)
	}
}

// A max_tokens above what the hold can cover is sent as the ceiling the hold was
// reserved for, and nothing else about the request moves.
func TestNativeMaxTokensIsTheReservedCeiling(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, globalProviderOwner, url)

	req := strings.Replace(maximal, `"max_tokens":   2048`, `"max_tokens":   640000`, 1)
	p.post(t, req)

	_, _, sentBody, _ := got.get()
	want := strings.Replace(addressed(t, req), `"max_tokens":   640000`, `"max_tokens":   4096`, 1)
	if sentBody != want {
		t.Errorf("max_tokens was not held to the reserved ceiling.\n got: %s\nwant: %s", sentBody, want)
	}
}

// A body this process and the upstream could read differently is refused before it
// is sent: it would be routed and priced on one reading and answered on the other.
func TestNativeRefusesAnAmbiguousBody(t *testing.T) {
	cases := map[string]string{
		"a repeated key":        strings.Replace(maximal, `"stream": false`, `"stream": false, "stream": true`, 1),
		"a key in other case":   strings.Replace(maximal, `"stream": false`, `"Stream": false`, 1),
		"a long-s key":          strings.Replace(maximal, `"stream": false`, `"ſtream": false`, 1),
		"a second model":        strings.Replace(maximal, `"stream": false`, `"stream": false, "MODEL": "claude-opus-4"`, 1),
		"a Kelvin-sign key":     strings.Replace(maximal, `"thinking": {`, `"thin`+"K"+`ing": {`, 1),
		"a second max_tokens":   strings.Replace(maximal, `"top_k": 40`, `"top_k": 40, "max_tokens": 128000`, 1),
		"trailing after object": maximal + ` {}`,
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			url, got := nativeUpstream(t, "application/json", nativeReply)
			p := nativePayer(t, globalProviderOwner, url)
			c, body := p.post(t, req)
			if answered(c) != http.StatusBadRequest || !strings.Contains(body, "invalid_request_error") {
				t.Errorf("status %d, want 400 invalid_request_error: %s", answered(c), body)
			}
			if calls, _, _, _ := got.get(); calls != 0 {
				t.Errorf("an ambiguous body reached the upstream")
			}
		})
	}
}

// On the shared account only what is priced in tokens and touches no state the
// account holds is served: anything else is refused before anything is sent — a
// field, value, tool or beta off the list, a file the account holds, and a tool
// whose type is written twice, escaped, or in another case so that encoding/json
// would keep a harmless one.
func TestSharedAccountServesOnlyWhatItPrices(t *testing.T) {
	bash := `{"type": "bash_20250124", "name": "bash"}`
	stream := `"stream": false`
	tool := func(s string) string { return strings.Replace(maximal, bash, s, 1) }
	field := func(s string) string { return strings.Replace(maximal, stream, stream+", "+s, 1) }
	fields := map[string]string{
		"fast mode":                    strings.Replace(maximal, `"speed": "standard"`, `"speed": "fast"`, 1),
		"a pinned region":              strings.Replace(maximal, `"inference_geo": "global"`, `"inference_geo": "us"`, 1),
		"priority tier":                strings.Replace(maximal, `"service_tier": "standard_only"`, `"service_tier": "priority"`, 1),
		"server fallbacks":             field(`"fallbacks": "default"`),
		"a container":                  field(`"container": "container_011"`),
		"a remote MCP server":          field(`"mcp_servers": [{"type": "url", "url": "https://mcp.example.com/sse", "name": "ex"}]`),
		"cache diagnostics":            field(`"diagnostics": {"previous_message_id": "msg_OTHER_TENANT"}`),
		"an unpublished field":         field(`"x_unreleased": 1`),
		"web search":                   tool(`{"type": "web_search_20260209", "name": "web_search"}`),
		"code execution":               tool(`{"type": "code_execution_20260521", "name": "code_execution"}`),
		"code execution 01":            tool(`{"type": "code_execution_20260120", "name": "code_execution"}`),
		"the advisor":                  tool(`{"type": "advisor_20260301", "name": "advisor", "model": "claude-opus-5-5"}`),
		"an MCP toolset":               tool(`{"type": "mcp_toolset", "mcp_server_name": "ex"}`),
		"a type written twice":         tool(`{"type": "bash_20250124", "name": "web_search", "type": "web_search_20260209"}`),
		"a type in two cases":          tool(`{"type": "web_search_20260209", "Type": "bash_20250124", "name": "web_search"}`),
		"an escaped key":               tool(`{"type": "web_search_20260209", "name": "web_search"}`),
		"an escaped value":             tool(`{"type": "web_search_20260209", "name": "web_search"}`),
		"a held file":                  strings.Replace(maximal, `"source": {"type": "url", "url": "https://example.com/a.pdf"}`, `"source": {"type": "file", "file_id": "file_011"}`, 1),
		"a held file in a tool result": strings.Replace(maximal, `"content": "18C"`, `"content": [{"type": "document", "source": {"file_id": "file_011", "type": "file"}}]`, 1),
		"a container upload":           strings.Replace(maximal, `{"type": "text", "text": "You are terse."`, `{"type": "container_upload", "file_id": "file_011"}, {"type": "text", "text": "You are terse."`, 1),
	}
	for name, req := range fields {
		t.Run(name, func(t *testing.T) {
			url, got := nativeUpstream(t, "application/json", nativeReply)
			p := nativePayer(t, globalProviderOwner, url)
			c, body := p.post(t, req)
			if answered(c) != http.StatusForbidden || !strings.Contains(body, "permission_error") {
				t.Errorf("status %d, want 403 permission_error: %s", answered(c), body)
			}
			if calls, _, _, _ := got.get(); calls != 0 {
				t.Errorf("an unpriced request reached the upstream on the shared account")
			}
			if _, reserved, _, _ := object.GlobalBalanceLedger.Snapshot(p.subject); reserved != 0 {
				t.Errorf("a refused request left %d cents reserved", reserved)
			}
		})
	}
	for _, beta := range []string{"context-1m-2025-08-07", "files-api-2025-04-14", "cache-diagnosis-2026-04-07",
		"mcp-client-2025-11-20", "fast-mode-2026-02-01", "some-future-paid-beta-2027-01-01"} {
		t.Run(beta, func(t *testing.T) {
			url, got := nativeUpstream(t, "application/json", nativeReply)
			p := nativePayer(t, globalProviderOwner, url)
			c := as(visit("POST", "/v1/messages"), p.credential)
			c.Fiber().Request().Header.Add("anthropic-beta", "interleaved-thinking-2025-05-14, "+beta)
			c.Fiber().Request().SetBody([]byte(maximal))
			c.AnthropicMessages()
			if answered(c) != http.StatusForbidden || !strings.Contains(sent(c), beta) {
				t.Errorf("status %d, want 403 naming %s: %s", answered(c), beta, sent(c))
			}
			if calls, _, _, _ := got.get(); calls != 0 {
				t.Errorf("an unpriced beta reached the upstream on the shared account")
			}
		})
	}
}

// Compaction, the computer toolset and tool search are priced in tokens, compaction
// per iteration, so the shared account sends them.
func TestSharedAccountServesWhatItPrices(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, globalProviderOwner, url)
	req := strings.Replace(maximal, `"context_management": {"edits": [{"type": "clear_tool_uses_20250919"}]}`,
		`"context_management": {"edits": [{"type": "compact_20260112", "trigger": {"type": "input_tokens", "value": 50000}}]}`, 1)
	req = strings.Replace(req, `{"type": "bash_20250124", "name": "bash"}`,
		`{"type": "computer_toolset_20260801"}, {"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"}`, 1)
	c := as(visit("POST", "/v1/messages"), p.credential)
	c.Fiber().Request().Header.Add("anthropic-beta", "compact-2026-01-12,context-management-2025-06-27")
	c.Fiber().Request().SetBody([]byte(req))
	c.AnthropicMessages()
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if _, _, sentBody, _ := got.get(); sentBody != addressed(t, req) {
		t.Errorf("the upstream was not sent the caller's bytes: %s", sentBody)
	}
}

// On the payer's own connected account the upstream bills the payer and holds only
// the payer's state, so everything crosses as written, any beta included.
func TestOwnAccountSendsEverythingAsWritten(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, "acme", url)

	c := as(visit("POST", "/v1/messages"), p.credential)
	c.Fiber().Request().Header.Add("anthropic-beta", "files-api-2025-04-14,some-future-paid-beta-2027-01-01")
	c.Fiber().Request().SetBody([]byte(everything))
	c.AnthropicMessages()
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	_, _, sentBody, header := got.get()
	if sentBody != addressed(t, everything) {
		t.Errorf("the payer's own account was not sent the payer's bytes: %s", sentBody)
	}
	if v := header.Values("Anthropic-Beta"); len(v) != 1 || v[0] != "files-api-2025-04-14,some-future-paid-beta-2027-01-01" {
		t.Errorf("anthropic-beta = %q", v)
	}
}

// The ZAP surface sends the same bytes: only the model changes, and stream is off
// because one frame carries one answer.
func TestZapNativeUpstreamReceivesTheCallersBytes(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, globalProviderOwner, url)

	req := strings.Replace(maximal, `"stream": false`, `"stream": true`, 1)
	status, body, _ := zapAnthropicMessages(context.Background(), p.credential, []byte(req))
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	_, _, sentBody, header := got.get()
	if want := strings.Replace(addressed(t, req), `"stream": true`, `"stream": false`, 1); sentBody != want {
		t.Errorf("the upstream was not sent the caller's bytes.\n got: %s\nwant: %s", sentBody, want)
	}
	if v := header.Get("Anthropic-Version"); v != anthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", v, anthropicVersion)
	}
	p.billed(t)
}

func TestSpliceChangesOnlyTheNamedValues(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"compact", `{"model":"a","max_tokens":5}`, `{"model":"b","max_tokens":9}`},
		{"spaced", "{ \"max_tokens\" :\t5 ,\n\"model\"  :  \"a\" , \"x\":[1, 2.0e1] }\n", "{ \"max_tokens\" :\t9 ,\n\"model\"  :  \"b\" , \"x\":[1, 2.0e1] }\n"},
		{"escaped key", `{"model":"a","max_tokens":5}`, `{"model":"b","max_tokens":9}`},
		{"nested model kept", `{"tools":[{"model":"a"}],"system":"\"model\": \"a\"","model":"a","max_tokens":5}`, `{"tools":[{"model":"a"}],"system":"\"model\": \"a\"","model":"b","max_tokens":9}`},
		{"unrelied casing passes", `{"Metadata":{"User_Id":"x"},"model":"a","max_tokens":5}`, `{"Metadata":{"User_Id":"x"},"model":"b","max_tokens":9}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splice([]byte(tc.in), map[string]any{"model": "b", "max_tokens": 9})
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestSpliceRefusesWhatItCannotAddress(t *testing.T) {
	for name, in := range map[string]string{
		"missing key":   `{"max_tokens":5}`,
		"not an object": `["model"]`,
		"trailing data": `{"model":"a","max_tokens":5} {}`,
		"duplicate":     `{"model":"a","max_tokens":5,"model":"c"}`,
		"folded":        `{"model":"a","max_tokens":5,"MAX_TOKENS":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := splice([]byte(in), map[string]any{"model": "b", "max_tokens": 9}); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// fold agrees with strings.EqualFold, the equality encoding/json matches a key to a
// field by.
func TestFoldIsEqualFold(t *testing.T) {
	pairs := [][2]string{{"stream", "STREAM"}, {"stream", "ſtream"}, {"thinking", "thinKing"},
		{"model", "modeL"}, {"model", "mode1"}, {"top_k", "top_K"}, {"ß", "SS"}}
	for _, p := range pairs {
		if (fold(p[0]) == fold(p[1])) != strings.EqualFold(p[0], p[1]) {
			t.Errorf("fold(%q)==fold(%q) is %v; EqualFold says %v", p[0], p[1], fold(p[0]) == fold(p[1]), strings.EqualFold(p[0], p[1]))
		}
	}
}

// upstreamWith answers every request with fn, recording what it was sent.
func upstreamWith(t *testing.T, fn func(w http.ResponseWriter, r *http.Request)) (string, *received) {
	t.Helper()
	got := &received{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.calls++
		got.path, got.body, got.header = r.URL.Path, string(b), r.Header.Clone()
		got.mu.Unlock()
		fn(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, got
}

// spentBy is what the payer's ledger has been charged, and what it still holds.
func spentBy(p payer) (spent, reserved int64) {
	balance, reserved, _, _ := object.GlobalBalanceLedger.Snapshot(p.subject)
	return p.funded - balance, reserved
}

// The vendor's headers stay with the vendor: its organization, its rate limits,
// its request id and its cookies. The client gets the answer's type and our id.
func TestUpstreamHeadersStayUpstream(t *testing.T) {
	url, _ := upstreamWith(t, func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("anthropic-organization-id", "org-shared-0000")
		h.Set("request-id", "req_upstream_0001")
		h.Set("anthropic-ratelimit-input-tokens-remaining", "12345")
		h.Set("Set-Cookie", "__cf_bm=abc; path=/")
		h.Set("cf-ray", "8f00-SJC")
		_, _ = io.WriteString(w, nativeReply)
	})
	p := nativePayer(t, globalProviderOwner, url)
	c, body := p.post(t, maximal)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), body)
	}
	rh := &c.Fiber().Response().Header
	for _, k := range []string{"anthropic-organization-id", "request-id", "anthropic-ratelimit-input-tokens-remaining", "Set-Cookie", "cf-ray"} {
		if v := rh.Peek(k); len(v) > 0 {
			t.Errorf("the upstream's %s reached the client: %s", k, v)
		}
	}
	if ct := string(rh.ContentType()); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	ev := p.usage()
	if id := string(rh.Peek("X-Request-Id")); len(ev) != 1 || id == "" || id != ev[0].RequestID {
		t.Errorf("x-request-id = %q, want the usage row's id (%+v)", id, ev)
	}
}

// A stream that ends without the vendor's final count — the upstream cut off, an
// error event mid-way, or silence past the idle deadline — is billed at no less than
// its reservation, and gives the reservation back.
func TestACutStreamBillsItsReservation(t *testing.T) {
	prev := upstreamIdle
	upstreamIdle = 300 * time.Millisecond
	t.Cleanup(func() { upstreamIdle = prev })

	start := strings.SplitN(nativeStream, "event: content_block_start", 2)[0]
	deltas := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, start)
		for i := 0; i < 2000; i++ {
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"tok%d \"}}\n\n", i)
		}
	}
	for name, fn := range map[string]func(w http.ResponseWriter, r *http.Request){
		"connection closed": func(w http.ResponseWriter, r *http.Request) { deltas(w) },
		"mid-stream error": func(w http.ResponseWriter, r *http.Request) {
			deltas(w)
			_, _ = io.WriteString(w, "event: error\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`+"\n\n")
		},
		"stalled": func(w http.ResponseWriter, r *http.Request) {
			deltas(w)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		},
	} {
		t.Run(name, func(t *testing.T) {
			url, _ := upstreamWith(t, fn)
			p := nativePayer(t, globalProviderOwner, url)
			req := strings.Replace(maximal, `"stream": false`, `"stream": true`, 1)
			c, body := p.post(t, req)
			if answered(c) != http.StatusOK || strings.Count(body, "text_delta") != 2000 {
				t.Fatalf("status %d, %d deltas relayed", answered(c), strings.Count(body, "text_delta"))
			}
			floor := reservation([]byte(req), 2048)
			want := calculateCostCentsWithCache(nativeSKU, max(1200, floor.InputTokens), 2048, 5000, 300)
			spent, reserved := spentBy(p)
			if spent != want || spent < holdCents(nativeSKU, floor) {
				t.Errorf("a cut stream was billed %d cents; its reservation is %d, want %d", spent, holdCents(nativeSKU, floor), want)
			}
			if reserved != 0 {
				t.Errorf("%d cents still reserved", reserved)
			}
		})
	}
}

// A line past sseLineMax — a document inside a tool result — is relayed whole, and
// the count after it is read and billed.
func TestALongSSELineIsRelayedAndBilled(t *testing.T) {
	big := strings.Repeat("A", 1100*1024)
	line := `data: {"type":"content_block_start","index":1,"content_block":{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + big + `"}}}`
	url, _ := upstreamWith(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.SplitN(nativeStream, "event: content_block_start", 2)[0])
		_, _ = io.WriteString(w, "event: content_block_start\n"+line+"\r\n\n")
		_, _ = io.WriteString(w, ": a keep-alive "+strings.Repeat("x", 1100*1024)+"\n\n")
		_, _ = io.WriteString(w, "event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":400000,"output_tokens":9000}}`+"\n\n"+
			"event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
	})
	p := nativePayer(t, globalProviderOwner, url)
	_, body := p.post(t, strings.Replace(maximal, `"stream": false`, `"stream": true`, 1))
	if !strings.Contains(body, "\n"+line+"\n\n") {
		t.Error("the long line did not reach the client whole")
	}
	if !strings.Contains(body, ":\n\nevent: message_delta") {
		t.Error("a long comment was not reduced to its keep-alive")
	}
	if !strings.HasSuffix(body, "event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n") {
		t.Error("the stream stopped at the long line")
	}
	if spent, _ := spentBy(p); spent != calculateCostCentsWithCache(nativeSKU, 400000, 9000, 5000, 300) {
		t.Errorf("billed %d cents, want the count after the long line", spent)
	}
}

// The reservation reads the prompt's size from the body: a prompt the balance
// cannot cover is refused before the vendor is asked.
func TestTheHoldCoversTheBody(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, globalProviderOwner, url)
	req := strings.Replace(maximal, `"You are terse."`, `"`+strings.Repeat("word ", 140000)+`"`, 1)
	hold := holdCents(nativeSKU, reservation([]byte(req), 2048))
	object.GlobalBalanceLedger.SetBalance(p.subject, hold-1)

	c, body := p.post(t, req)
	if answered(c) != http.StatusPaymentRequired {
		t.Errorf("status %d with %d cents against a %d cent hold: %s", answered(c), hold-1, hold, body)
	}
	if calls, _, _, _ := got.get(); calls != 0 {
		t.Error("the vendor was asked for a prompt the balance cannot cover")
	}
	if hold < calculateCostCents(nativeSKU, 200000, 2048) {
		t.Errorf("a 700 KB prompt held %d cents", hold)
	}
}

// Compaction runs the model more than once; every run is billed, not only the last.
func TestEveryIterationIsBilled(t *testing.T) {
	reply := `{"id":"msg_x","type":"message","role":"assistant","model":"` + nativeID + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":20000,"output_tokens":500,"iterations":[{"type":"compaction","input_tokens":180000,"output_tokens":4000},{"type":"message","input_tokens":20000,"output_tokens":500}]}}`
	url, _ := nativeUpstream(t, "application/json", reply)
	p := nativePayer(t, globalProviderOwner, url)
	p.post(t, maximal)
	if spent, _ := spentBy(p); spent != calculateCostCents(nativeSKU, 200000, 4500) {
		t.Errorf("billed %d cents, want every iteration's %d", spent, calculateCostCents(nativeSKU, 200000, 4500))
	}
}

// A cache write costs 1.25x input kept five minutes and 2x kept an hour.
func TestCacheWritesArePricedByTTL(t *testing.T) {
	if w, in := calculateCostCentsWithCache(nativeSKU, 0, 0, 0, 1_000_000), calculateCostCents(nativeSKU, 1_000_000, 0); w*100 != in*125 {
		t.Errorf("1M five-minute writes cost %d cents against %d of input", w, in)
	}
	reply := `{"id":"msg_x","type":"message","role":"assistant","model":"` + nativeID + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":1000000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":1000000}}}`
	url, _ := nativeUpstream(t, "application/json", reply)
	p := nativePayer(t, globalProviderOwner, url)
	p.post(t, maximal)
	if spent, _ := spentBy(p); spent != calculateCostCents(nativeSKU, 2_000_000, 0) {
		t.Errorf("1M one-hour writes billed %d cents, want 2x input: %d", spent, calculateCostCents(nativeSKU, 2_000_000, 0))
	}
}

// A request with no tools and no media, to a native upstream, is the caller's own
// request too: every turn, the system blocks, thinking, stop sequences and the
// caller's max_tokens.
func TestATextRequestArrivesAsSent(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, globalProviderOwner, url)
	req := `{"model": "claude-opus-4", "max_tokens": 20000, "stream": false,
  "system": [{"type": "text", "text": "SYS-PROMPT", "cache_control": {"type": "ephemeral"}}],
  "thinking": {"type": "enabled", "budget_tokens": 8000},
  "stop_sequences": ["STOP"], "temperature": 0.2, "metadata": {"user_id": "u1"},
  "messages": [
    {"role": "user", "content": "FIRST-USER-TURN"},
    {"role": "assistant", "content": "FIRST-ASSISTANT-TURN"},
    {"role": "user", "content": [{"type": "text", "text": "SECOND-USER-TURN", "cache_control": {"type": "ephemeral"}}]}
  ]}`
	c, body := p.post(t, req)
	if answered(c) != http.StatusOK || body != nativeReply {
		t.Fatalf("status %d: %s", answered(c), body)
	}
	if _, path, sentBody, _ := got.get(); path != "/v1/messages" || sentBody != addressed(t, req) {
		t.Errorf("the upstream was not sent the caller's bytes at /v1/messages (%s): %s", path, sentBody)
	}
}

// Concurrent streams settle once each: every answer billed, nothing left held.
func TestConcurrentStreamsSettleOnce(t *testing.T) {
	url, _ := nativeUpstream(t, "text/event-stream", nativeStream)
	p := nativePayer(t, globalProviderOwner, url)
	req := strings.Replace(maximal, `"stream": false`, `"stream": true`, 1)
	const n = 16
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.post(t, req)
		}()
	}
	wg.Wait()
	if spent, reserved := spentBy(p); spent != n*nativeCost() || reserved != 0 || len(p.usage()) != n {
		t.Errorf("spent %d want %d, reserved %d, events %d", spent, n*nativeCost(), reserved, len(p.usage()))
	}
}

// A family's streamed answer settles its hold at the answer's cost. The request's
// own deferred settle(0) runs when the handler returns — before the stream — and
// used to take the one settle a hold gets.
func TestAFamilyStreamSettlesItsHoldAtItsCost(t *testing.T) {
	const stream = `data: {"id":"g","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n" +
		`data: {"id":"g","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100000,"completion_tokens":10000,"total_tokens":110000}}` + "\n\n" +
		"data: [DONE]\n\n"
	fam := otherFamily(t, streaming(t, stream).URL)
	fam.byID = map[string]zenModel{"enso-big": {ID: "enso-big", Base: zenTier{In: decimal.New(15, 0), Out: decimal.New(75, 0)}}}
	fam.ids = []string{"enso-big"}

	const subject = "acme/fam"
	object.GlobalBalanceLedger.SetBalance(subject, 100000)
	hold, ok := reserveBudget(subject, 300)
	if !ok {
		t.Fatal("reserve refused")
	}
	body := []byte(`{"model":"enso-big","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(fam, "chat/completions", "openai", "enso-big", body, true, 0, "acme", nil, false, hold, time.Now()); out != nil {
		t.Fatalf("refused: %+v", out)
	}
	hold.settle(0) // the handler's deferred settle
	_ = sent(c)
	balance, reserved, _, _ := object.GlobalBalanceLedger.Snapshot(subject)
	if spent := 100000 - balance; spent != 225 || reserved != 0 {
		t.Errorf("the family stream settled %d cents (want 225), %d still reserved", spent, reserved)
	}
}
