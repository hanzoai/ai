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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// claudeSKU is the SKU the cases ask for, as the resale catalog spells it, and
// claudeID what its maker calls it.
const (
	claudeSKU = "anthropic/claude-haiku-5.5"
	claudeID  = "claude-haiku-5-5"
)

// maker is a model vendor's own API on httptest: a list of models for each key,
// and a Messages endpoint that answers each key as the case says and remembers
// what it was sent.
type maker struct {
	*httptest.Server

	mu sync.Mutex
	// lists is the status GET /v1/models answers a key with, and refuse what
	// POST /v1/messages answers it with; a key in neither is answered 200.
	lists  map[string]int
	refuse map[string]turnedAway
	// events is the stream a 200 answers with; nil for one that says "ok".
	events []string
	// whole answers a request for a stream with one JSON body.
	whole  bool
	asked  []string // the key of each Messages call, in order
	bodies []string
	heads  []http.Header
	read   []string // the key of each list read
}

// turnedAway is what the maker answers a key with in place of an answer.
type turnedAway struct {
	status  int
	kind    string
	message string
	after   string // Retry-After
}

// makerModels is what every key lists: one model that thinks adaptively and takes
// no sampling parameter, one dated release that thinks within a budget.
const makerModels = `{"data":[
 {"type":"model","id":"claude-haiku-5-5","max_tokens":128000,"capabilities":{
   "image_input":{"supported":true},"structured_outputs":{"supported":true},
   "thinking":{"supported":true,"types":{"enabled":{"supported":false},"adaptive":{"supported":true}}},
   "effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":{"supported":false},"max":{"supported":true}}}},
 {"type":"model","id":"claude-haiku-4-5-20251001","max_tokens":64000,"capabilities":{
   "image_input":{"supported":true},"structured_outputs":{"supported":false},
   "thinking":{"supported":true,"types":{"enabled":{"supported":true},"adaptive":{"supported":false}}},
   "effort":{"supported":false}}},
 {"type":"model","id":"claude-haiku-4-5-20250101","max_tokens":64000,"capabilities":{}},
 {"type":"model","id":"claude-free-1","max_tokens":4096,"capabilities":{}}
],"has_more":false,"last_id":"claude-free-1"}`

// okEvents is the stream of an answer that says "ok": 1,000 prompt tokens read
// fresh, 3,000 read from the cache, 2,000 written to it, and 500 written out.
var okEvents = []string{
	`{"type":"message_start","message":{"id":"msg_vendor","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":1000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":3000,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"ping"}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":500}}`,
	`{"type":"message_stop"}`,
}

func (m *maker) serve(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("x-api-key")
	if r.URL.Path == "/v1/models" {
		m.mu.Lock()
		m.read = append(m.read, key)
		status := m.lists[key]
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		_, _ = io.WriteString(w, makerModels)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var in struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(b, &in)
	m.mu.Lock()
	m.asked = append(m.asked, key)
	m.bodies = append(m.bodies, string(b))
	m.heads = append(m.heads, r.Header.Clone())
	no, refused := m.refuse[key]
	events := m.events
	if m.whole {
		in.Stream = false
	}
	m.mu.Unlock()
	if refused {
		w.Header().Set("Content-Type", "application/json")
		if no.after != "" {
			w.Header().Set("Retry-After", no.after)
		}
		w.WriteHeader(no.status)
		fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":%q}}`, no.kind, no.message)
		return
	}
	if events == nil {
		events = okEvents
	}
	if !in.Stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_vendor","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":3000,"output_tokens":500}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var typ struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &typ)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.Type, e)
	}
}

func (m *maker) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.asked)
}

// sentBody is the body of the maker's i-th Messages call, decoded.
func (m *maker) sentBody(t *testing.T, i int) map[string]json.RawMessage {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if i >= len(m.bodies) {
		t.Fatalf("the maker was sent %d calls, want more than %d", len(m.bodies), i)
	}
	return decoded(t, []byte(m.bodies[i]))
}

// makerAt starts a maker, makes it Anthropic's address with keys under the
// reserved names in order, and reads what each key lists.
func makerAt(t *testing.T, m *maker, keys ...string) *maker {
	t.Helper()
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	t.Setenv("ANTHROPIC_URL", m.URL)
	for i, name := range object.AnthropicKeys {
		key := ""
		if i < len(keys) {
			key = keys[i]
		}
		t.Setenv(name, key)
	}
	t.Setenv(object.AnthropicKeyList, "")
	forgetKeys()
	cooled.forget()
	anthropicNative.forget()
	t.Cleanup(func() {
		forgetKeys()
		cooled.forget()
		anthropicNative.forget()
	})
	anthropicNative.refresh()
	return m
}

// resale points the resale family at a vendor that answers each of its keys as
// the case says, listing the Claude SKU at the vendor's price plus a 5.5% fee, a free
// route for the pool, and whatever else a case names.
func resale(t *testing.T, a *accounts, also ...string) *modelFamily {
	t.Helper()
	restore(t, engineFam)
	engineFam.urlKey = "TEST_ENGINE_URL_UNSET"
	engineFam.providerFn = nil
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "")
	t.Setenv("OPENROUTER_API_KEY_3", "")
	fam := spareFamily(t, a.serve(t).URL, "vendor/big:free", also...)
	// $0.10 in, $0.50 out, $0.01 a cached read and $0.125 a cache write a million
	// tokens at the vendor, each times 1.055.
	tier := zenTier{
		MaxCtx: 1_000_000,
		In:     decimal.MustParse("0.1055"), Out: decimal.MustParse("0.5275"),
		CacheRead: decimal.MustParse("0.01055"), CacheWrite: decimal.MustParse("0.131875"),
	}
	fam.byID[claudeSKU] = zenModel{ID: claudeSKU, Margin: decimal.MustParse("1.055"), Base: tier, Tiers: []zenTier{tier}}
	fam.ids = append(fam.ids, claudeSKU)
	return fam
}

// chat asks fam for one chat completion of sku through the pipe and returns the
// controller, the refusals the pipe handed back, and what the caller was sent.
func chat(t *testing.T, c *ApiController, fam *modelFamily, sku, body string, stream bool, user *iam.User) ([]attempt, string) {
	t.Helper()
	c.Fiber().Request().SetBody([]byte(body))
	out := c.pipeToFamily(fam, "chat/completions", "openai", sku, []byte(body), stream, 0, "acme", user, false, nil, time.Now())
	return out, drain(t, c)
}

func hello(sku string) string {
	return `{"model":"` + sku + `","messages":[{"role":"user","content":"2+2?"}]}`
}

// A Claude model is asked of its maker before the reseller that lists it, on each
// key in the order the pool names them: a key the vendor refuses hands the same
// request to the next, and the reseller is never asked once one answers. The
// caller is answered as the model it named.
func TestAClaudeModelIsAskedOfItsMakerOnEveryKeyInOrder(t *testing.T) {
	or := &accounts{}
	fam := resale(t, or)
	m := makerAt(t, &maker{refuse: map[string]turnedAway{
		"a1": {status: 401, kind: "authentication_error", message: "invalid x-api-key"},
		"a2": {status: 429, kind: "rate_limit_error", message: "slow down", after: "30"},
	}}, "a1", "a2", "a3")

	c := visit(http.MethodPost, "/v1/chat/completions")
	out, answer := chat(t, c, fam, claudeSKU, hello(claudeSKU), false, nil)
	if out != nil || !strings.Contains(answer, `"content":"ok"`) {
		t.Fatalf("attempts=%+v answer=%s — the third key should have served", out, answer)
	}
	if got := m.calls(); !slices.Equal(got, []string{"a1", "a2", "a3"}) {
		t.Fatalf("the maker's keys were asked %v, want [a1 a2 a3]", got)
	}
	if got := or.calls(); len(got) != 0 {
		t.Fatalf("the reseller was asked %v after the maker answered", got)
	}
	if got := header(c, servedHeader); got != claudeSKU {
		t.Errorf("%s = %q, want the model asked for", servedHeader, got)
	}
	var whole struct {
		Model string `json:"model"`
		Usage struct {
			Prompt  int `json:"prompt_tokens"`
			Details struct {
				Cached  int `json:"cached_tokens"`
				Written int `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(answer)), &whole); err != nil {
		t.Fatalf("the answer is not one chat completion: %v: %s", err, answer)
	}
	if whole.Model != claudeSKU || whole.Usage.Prompt != 6000 || whole.Usage.Details.Cached != 3000 || whole.Usage.Details.Written != 2000 || whole.Usage.Completion != 500 {
		t.Errorf("answer = %+v, want the model asked for and a prompt of 6000 with 3000 read from the cache and 2000 written to it", whole)
	}
	// What each key is asked for is the vendor's own id, with the vendor's
	// headers, and never another vendor's field.
	for i := range m.calls() {
		b := m.sentBody(t, i)
		if string(b["model"]) != `"`+claudeID+`"` || b["provider"] != nil {
			t.Errorf("call %d was sent model %s provider %s", i, b["model"], b["provider"])
		}
		if v := m.heads[i].Get("anthropic-version"); v != anthropicVersion {
			t.Errorf("call %d carried anthropic-version %q", i, v)
		}
		if m.heads[i].Get("Authorization") != "" {
			t.Errorf("call %d carried an Authorization header", i)
		}
	}
	// The refused key sits out; the limited one until the vendor's word, no longer.
	accts := anthropicNative.accounts()
	if !cooling(accts[0].Key, false) || !cooling(accts[1].Key, false) || cooling(accts[2].Key, false) {
		t.Errorf("after 401, 429 and an answer the keys sit out %v %v %v, want true true false",
			cooling(accts[0].Key, false), cooling(accts[1].Key, false), cooling(accts[2].Key, false))
	}
}

// A key the vendor refused as a credential, or for want of credit, is not asked
// again until nativeDead has passed — and is asked again then. A rate-limited key
// is back when the vendor said it would be, and never later than nativeBusy.
func TestARefusedKeySitsOutItsTimeAndIsAskedAgain(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	keyNow = func() time.Time { return now }
	t.Cleanup(func() { keyNow = time.Now })

	or := &accounts{}
	fam := resale(t, or)
	m := makerAt(t, &maker{refuse: map[string]turnedAway{
		"a1": {status: 403, kind: "permission_error", message: "not allowed"},
		"a2": {status: 400, kind: "invalid_request_error", message: "Your credit balance is too low to access the API."},
		"a3": {status: 429, kind: "rate_limit_error", message: "slow down", after: "600"},
	}}, "a1", "a2", "a3")
	ask := func() []string {
		t.Helper()
		m.mu.Lock()
		m.asked = nil
		m.mu.Unlock()
		or.reset()
		c := visit(http.MethodPost, "/v1/chat/completions")
		if out, answer := chat(t, c, fam, claudeSKU, hello(claudeSKU), false, nil); out != nil || !strings.Contains(answer, `"ok"`) {
			t.Fatalf("attempts=%+v answer=%s — the reseller should have served", out, answer)
		}
		if got := or.calls(); !slices.Equal(got, []string{"k1"}) {
			t.Fatalf("the reseller was asked %v, want once", got)
		}
		return m.calls()
	}

	if got := ask(); !slices.Equal(got, []string{"a1", "a2", "a3"}) {
		t.Fatalf("asked %v, want every key once", got)
	}
	if got := ask(); len(got) != 0 {
		t.Fatalf("asked %v while every key sits out", got)
	}
	// A minute on, the limited key is back whatever the vendor said; the refused
	// two are not.
	now = now.Add(nativeBusy + time.Second)
	if got := ask(); !slices.Equal(got, []string{"a3"}) {
		t.Fatalf("asked %v a minute on, want the rate-limited key alone", got)
	}
	now = now.Add(nativeDead)
	if got := ask(); !slices.Equal(got, []string{"a1", "a2", "a3"}) {
		t.Fatalf("asked %v once the wait was over, want every key probed again", got)
	}

	// A key whose list reads again is back at once.
	m.mu.Lock()
	m.refuse = nil
	m.mu.Unlock()
	anthropicNative.refresh()
	if got := askOnce(t, m, fam); !slices.Equal(got, []string{"a1"}) {
		t.Fatalf("asked %v after the keys' lists read again, want the first key to serve", got)
	}
}

// A key's list reading again says the vendor accepts the key, and nothing of what
// its account can pay: a key resting for want of credit rests on through it, and
// the next key serves.
func TestAKeyOutOfCreditRestsThoughItsListReads(t *testing.T) {
	or := &accounts{}
	fam := resale(t, or)
	m := makerAt(t, &maker{refuse: map[string]turnedAway{
		"a1": {status: 400, kind: "invalid_request_error", message: "Your credit balance is too low to access the API."},
	}}, "a1", "a2")
	if got := askOnce(t, m, fam); !slices.Equal(got, []string{"a1", "a2"}) {
		t.Fatalf("asked %v, want the key out of credit and then the next", got)
	}
	anthropicNative.refresh()
	if got := askOnce(t, m, fam); !slices.Equal(got, []string{"a2"}) {
		t.Fatalf("asked %v after the lists read again, want the funded key alone", got)
	}
	if got := or.calls(); len(got) != 0 {
		t.Fatalf("the reseller was asked %v while a key of the maker's served", got)
	}
}

// askOnce asks once and returns the maker's keys asked, for an answer the maker gives.
func askOnce(t *testing.T, m *maker, fam *modelFamily) []string {
	t.Helper()
	m.mu.Lock()
	m.asked = nil
	m.mu.Unlock()
	c := visit(http.MethodPost, "/v1/chat/completions")
	if out, answer := chat(t, c, fam, claudeSKU, hello(claudeSKU), false, nil); out != nil || !strings.Contains(answer, `"ok"`) {
		t.Fatalf("attempts=%+v answer=%s", out, answer)
	}
	return m.calls()
}

// A key the vendor refuses when its list is read is never asked for an answer,
// and one whose list was never read is not asked either.
func TestAKeyIsAskedOnlyForWhatItLists(t *testing.T) {
	or := &accounts{}
	fam := resale(t, or)
	m := makerAt(t, &maker{lists: map[string]int{"a1": 401, "a2": 500}}, "a1", "a2", "a3")
	if got := askOnce(t, m, fam); !slices.Equal(got, []string{"a3"}) {
		t.Fatalf("asked %v, want only the key whose list was read", got)
	}

	n := anthropicNative
	accts := n.accounts()
	for sku, want := range map[string]string{
		claudeSKU:                          claudeID,
		"anthropic/claude-haiku-4.5":       "claude-haiku-4-5-20251001", // the newest release of a dated id
		"anthropic/claude-haiku-5.5:batch": "",                          // the reseller's own variant
		"anthropic/claude-opus-9":          "",                          // not listed
		"openai/gpt-6":                     "",                          // another vendor's
	} {
		got, ok := n.find(accts[2], sku)
		if got.ID != want || ok != (want != "") {
			t.Errorf("find(%q) = %q %v, want %q", sku, got.ID, ok, want)
		}
	}
	if !n.serves(claudeSKU) || n.serves("anthropic/claude-opus-9") {
		t.Error("serves disagrees with what the keys list")
	}
	if _, ok := n.find(accts[0], claudeSKU); ok {
		t.Error("a key the vendor refused lists a model")
	}
	if c := mustFind(t, accts[2], claudeSKU); !c.Adaptive || c.Budget || !c.Vision || !c.Schema || c.Out != 128000 || !c.Efforts["max"] || c.Efforts["xhigh"] {
		t.Errorf("the adaptive model read as %+v", c)
	}
	if c := mustFind(t, accts[2], "anthropic/claude-haiku-4.5"); c.Adaptive || !c.Budget || c.Schema || len(c.Efforts) != 0 {
		t.Errorf("the budget model read as %+v", c)
	}
}

func mustFind(t *testing.T, a object.Account, sku string) claude {
	t.Helper()
	c, ok := anthropicNative.find(a, sku)
	if !ok {
		t.Fatalf("%s is not listed", sku)
	}
	return c
}

// When no key of the maker's answers, the reseller is asked for the same model,
// under its own id for it, and its answer is the caller's. A refusal that is the
// request's own — one every key would give — goes to the reseller after one key.
func TestWhenNoKeyAnswersTheResellerIsAskedForTheSameModel(t *testing.T) {
	or := &accounts{}
	fam := resale(t, or)
	m := makerAt(t, &maker{refuse: map[string]turnedAway{
		"a1": {status: 529, kind: "overloaded_error", message: "Overloaded"},
		"a2": {status: 500, kind: "api_error", message: "Internal server error"},
	}}, "a1", "a2")

	c := visit(http.MethodPost, "/v1/chat/completions")
	out, answer := chat(t, c, fam, claudeSKU, hello(claudeSKU), false, nil)
	if out != nil || !strings.Contains(answer, `"ok"`) {
		t.Fatalf("attempts=%+v answer=%s — the reseller should have served", out, answer)
	}
	if got := m.calls(); !slices.Equal(got, []string{"a1", "a2"}) {
		t.Fatalf("the maker's keys were asked %v, want both", got)
	}
	if got := or.calls(); !slices.Equal(got, []string{"k1"}) {
		t.Fatalf("the reseller was asked %v, want once", got)
	}
	if b := decoded(t, []byte(or.bodies[0])); string(b["model"]) != `"`+claudeSKU+`"` {
		t.Fatalf("the reseller was asked for %s, want the model named", b["model"])
	}
	if got := header(c, servedHeader); got != claudeSKU {
		t.Errorf("%s = %q, want the model asked for", servedHeader, got)
	}
	// Neither an overloaded vendor nor a failing one benches a key.
	for _, a := range anthropicNative.accounts() {
		if cooling(a.Key, false) {
			t.Errorf("key %s sits out after a 5xx", a.Name)
		}
	}

	// The request's own refusal: one key hears it, then the reseller.
	m.mu.Lock()
	m.asked, m.refuse = nil, map[string]turnedAway{"a1": {status: 400, kind: "invalid_request_error", message: "max_tokens: too large"}}
	m.mu.Unlock()
	or.reset()
	if out, answer := chat(t, visit(http.MethodPost, "/v1/chat/completions"), fam, claudeSKU, hello(claudeSKU), false, nil); out != nil || !strings.Contains(answer, `"ok"`) {
		t.Fatalf("attempts=%+v answer=%s", out, answer)
	}
	if got := m.calls(); !slices.Equal(got, []string{"a1"}) {
		t.Fatalf("the maker's keys were asked %v for a refusal every key would give, want one", got)
	}
	if got := or.calls(); !slices.Equal(got, []string{"k1"}) {
		t.Fatalf("the reseller was asked %v, want once", got)
	}

	// An answer that is not the stream asked for is not relayed as one.
	m.mu.Lock()
	m.asked, m.refuse, m.whole = nil, nil, true
	m.mu.Unlock()
	or.reset()
	if out, answer := chat(t, visit(http.MethodPost, "/v1/chat/completions"), fam, claudeSKU, hello(claudeSKU), false, nil); out != nil || !strings.Contains(answer, `"ok"`) {
		t.Fatalf("attempts=%+v answer=%s", out, answer)
	}
	if got, sold := m.calls(), or.calls(); !slices.Equal(got, []string{"a1"}) || !slices.Equal(sold, []string{"k1"}) {
		t.Fatalf("a whole answer to a stream: the maker was asked %v and the reseller %v, want one key and then the reseller", got, sold)
	}
}

// A Claude model nobody could serve is refused as itself: the maker and the
// reseller are each asked for that model and no other, nothing is written under a
// 200, and the caller is told 503 providers_exhausted. No free route, Enso SKU or
// compute of ours answers in its name.
func TestAClaudeModelNobodyServesIsRefusedNeverStoodInFor(t *testing.T) {
	or := &accounts{status: map[string]int{"k1": http.StatusBadGateway}}
	fam := resale(t, or)
	m := makerAt(t, &maker{refuse: map[string]turnedAway{
		"a1": {status: 529, kind: "overloaded_error", message: "Overloaded"},
	}}, "a1")

	for _, stream := range []bool{false, true} {
		m.mu.Lock()
		m.asked, m.bodies = nil, nil
		m.mu.Unlock()
		or.reset()
		cooled.forget()
		c := visit(http.MethodPost, "/v1/chat/completions")
		out, answer := chat(t, c, fam, claudeSKU, hello(claudeSKU), stream, nil)
		if len(out) != 1 || out[0].fault != faultProvider || answer != "" || answered(c) != http.StatusOK && answered(c) != 0 {
			t.Fatalf("stream=%v: attempts=%+v answer=%q status=%d — want one refusal handed back and nothing written", stream, out, answer, answered(c))
		}
		var ae *apiError
		if err := exhausted(claudeSKU, out); !errors.As(err, &ae) || ae.status != http.StatusServiceUnavailable || ae.code != codeExhausted {
			t.Fatalf("stream=%v: the caller is told %+v, want 503 %s", stream, ae, codeExhausted)
		}
		if got := m.calls(); !slices.Equal(got, []string{"a1"}) {
			t.Fatalf("stream=%v: the maker was asked %v", stream, got)
		}
		for _, b := range append(slices.Clone(m.bodies), or.bodies...) {
			var in struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal([]byte(b), &in)
			if in.Model != claudeSKU && in.Model != claudeID {
				t.Fatalf("stream=%v: a vendor was asked for %q in place of %s", stream, in.Model, claudeSKU)
			}
		}
		// Nothing follows the family on the route either: the model has no
		// alternate, and the pool is not one.
		if queue := candidates("acme", fam.passthroughRoute(claudeSKU), out, false); len(queue) != 0 {
			t.Fatalf("stream=%v: the route offers %+v after the refusal", stream, queue)
		}
	}
}

// The free lane never reaches a maker. A request not seated on the paid lane, a
// platform whose paid switch is off, a route the family charges nothing for and
// the free pool's own door are each served, or refused, without the maker being
// asked — though every one of its keys is ready and lists the model.
func TestTheFreeLaneNeverReachesAMaker(t *testing.T) {
	const gratis = "anthropic/claude-free-1"
	or := &accounts{}
	fam := resale(t, or)
	fam.byID[gratis] = zenModel{ID: gratis}
	fam.ids = append(fam.ids, gratis)
	m := makerAt(t, &maker{}, "a1", "a2")
	if !anthropicNative.serves(claudeSKU) || !anthropicNative.serves(gratis) {
		t.Fatal("the maker lists neither model: the case proves nothing")
	}
	unasked := func(what string) {
		t.Helper()
		if got := m.calls(); len(got) != 0 {
			t.Fatalf("%s: the maker was asked %v", what, got)
		}
	}

	// No seat on the paid lane: a priced Claude model is refused, not sent.
	c := unseated(visit(http.MethodPost, "/v1/chat/completions"))
	if out, _ := chat(t, c, fam, claudeSKU, hello(claudeSKU), false, nil); out != nil || answered(c) < 400 {
		t.Fatalf("an unseated caller got attempts=%+v status=%d, want a refusal", out, answered(c))
	}
	unasked("an unseated caller")

	// The platform's paid switch off.
	FreeOnly = func() bool { return true }
	c = visit(http.MethodPost, "/v1/chat/completions")
	if out, _ := chat(t, c, fam, claudeSKU, hello(claudeSKU), false, nil); out != nil || answered(c) < 400 {
		t.Fatalf("with the paid lane off: attempts=%+v status=%d, want a refusal", out, answered(c))
	}
	FreeOnly = func() bool { return false }
	unasked("the paid lane off")

	// A route the family charges nothing for, by a seated caller.
	or.reset()
	c = visit(http.MethodPost, "/v1/chat/completions")
	if out, answer := chat(t, c, fam, gratis, hello(gratis), false, nil); out != nil || !strings.Contains(answer, `"ok"`) {
		t.Fatalf("a free route: attempts=%+v answer=%s", out, answer)
	}
	unasked("a free route")
	if got := or.calls(); !slices.Equal(got, []string{"k1"}) {
		t.Fatalf("a free route asked the family's vendor %v, want once", got)
	}

	// The pool's own door, seated or not.
	for _, seat := range []bool{true, false} {
		c = visit(http.MethodPost, "/v1/chat/completions")
		if !seat {
			c = unseated(c)
		}
		if out, answer := chat(t, c, fam, freeID, hello(freeID), false, nil); out != nil || !strings.Contains(answer, `"ok"`) {
			t.Fatalf("the pool, seated=%v: attempts=%+v answer=%s", seat, out, answer)
		}
		unasked("the free pool")
	}
}

// A maker's answer bills the catalog's price and nothing else: the tokens it read
// fresh, read from its cache and wrote to it, each at the SKU's own rate. What it
// cost is those tokens at the vendor's price, the family's fee taken off, so the
// margin of a call the maker served is on its row.
func TestAMakersAnswerBillsTheCatalogPriceAndStatesWhatItCost(t *testing.T) {
	or := &accounts{}
	fam := resale(t, or)
	makerAt(t, &maker{}, "a1")
	got := ledger(t)
	alice := &iam.User{Owner: "acme", Name: "alice"}

	// 1,000 fresh, 3,000 cached, 2,000 written, 500 out:
	//   vendor: (1000×0.10 + 3000×0.01 + 2000×0.125 + 500×0.50) / 1e6 = $0.00063
	//   caller: that × 1.055                                          = $0.00066465
	const cost, price = "0.00063", "0.00066465"
	for _, stream := range []bool{true, false} {
		c := visit(http.MethodPost, "/v1/chat/completions")
		if out, answer := chat(t, c, fam, claudeSKU, hello(claudeSKU), stream, alice); out != nil || !strings.Contains(answer, "ok") {
			t.Fatalf("stream=%v: attempts=%+v answer=%s", stream, out, answer)
		}
	}
	// The vendor's own dialect, whole and streamed, is priced the same.
	for _, stream := range []bool{false, true} {
		body := `{"model":"` + claudeSKU + `","max_tokens":64,"stream":` + strconv.FormatBool(stream) + `,"messages":[{"role":"user","content":"2+2?"}]}`
		c := visit(http.MethodPost, "/v1/messages")
		c.Fiber().Request().SetBody([]byte(body))
		if out := c.pipeToFamily(fam, "messages", "anthropic", claudeSKU, []byte(body), stream, 64, "acme", alice, false, nil, time.Now()); out != nil {
			t.Fatalf("messages stream=%v refused: %+v", stream, out)
		}
		if answer := drain(t, c); !strings.Contains(answer, "ok") || !strings.Contains(answer, claudeSKU) || strings.Contains(answer, "msg_vendor") {
			t.Fatalf("messages stream=%v answered %s, want the vendor's answer under our id and the model asked for", stream, answer)
		}
	}
	ev := got()
	if len(ev) != 4 {
		t.Fatalf("%d debits, want one an answer: %+v", len(ev), ev)
	}
	for i, e := range ev {
		if e.USD != price || e.CostUSD != cost || e.Model != claudeSKU || e.Provider != "openrouter" {
			t.Errorf("debit %d = $%s at a cost of $%s for %s via %s, want $%s at $%s for %s",
				i, e.USD, e.CostUSD, e.Model, e.Provider, price, cost, claudeSKU)
		}
	}
	if got := or.calls(); len(got) != 0 {
		t.Errorf("the reseller was asked %v", got)
	}
}

// An hour-long cache write costs the vendor's hour rate, which the catalog does
// not list: the call bills what it cost times the family's fee, never the
// five-minute rate.
func TestAnHourLongCacheWriteNeverBillsBelowItsCost(t *testing.T) {
	or := &accounts{}
	fam := resale(t, or)
	hour := slices.Clone(okEvents)
	hour[0] = `{"type":"message_start","message":{"id":"msg_vendor","role":"assistant","model":"claude-haiku-5-5","usage":{"input_tokens":1000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":0,"output_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2000}}}}`
	makerAt(t, &maker{events: hour}, "a1")
	got := ledger(t)
	alice := &iam.User{Owner: "acme", Name: "alice"}

	body := `{"model":"` + claudeSKU + `","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"2+2?"}]}`
	c := visit(http.MethodPost, "/v1/messages")
	c.Fiber().Request().SetBody([]byte(body))
	if out := c.pipeToFamily(fam, "messages", "anthropic", claudeSKU, []byte(body), true, 64, "acme", alice, false, nil, time.Now()); out != nil {
		t.Fatalf("refused: %+v", out)
	}
	_ = drain(t, c)
	ev := got()
	if len(ev) != 1 {
		t.Fatalf("%d debits: %+v", len(ev), ev)
	}
	billed, _ := usdNanos(ev[0].USD)
	spent, _ := usdNanos(ev[0].CostUSD)
	// 2,000 tokens written for an hour cost 2 × the input rate: $0.0004, and the
	// call $0.00075 with its fresh prompt and its answer.
	if spent < 750_000 || billed < spent || billed*1000 < spent*1055 {
		t.Fatalf("billed $%s at a cost of $%s, want the hour's cost (at least $0.00075) and the fee on top", ev[0].USD, ev[0].CostUSD)
	}
}

// An answer a maker served says where it came from: the maker's host and the place
// in its pool of the key that answered, and the same account on its span. Any other
// answer of the family's says the family's own address.
func TestAnAnswerSaysWhichOfTheMakersKeysServedIt(t *testing.T) {
	t.Setenv("ANTHROPIC_URL", "https://api.maker.example")
	prov := &object.Provider{Owner: "admin", Name: "openrouter", Type: "OpenRouter", ProviderUrl: "https://reseller.example/api"}
	h := http.Header{}
	h.Set(armHeader, claudeID)
	h.Set(providerHeader, "anthropic")
	h.Set(keyHeader, "ANTHROPIC_API_KEY_2")
	if origin, via := openrouterFam.reached(prov, nil, servingOf(h)); origin != "api.maker.example#2" || via != "anthropic#2" {
		t.Errorf("a maker's answer came from %q via %q, want api.maker.example#2 via anthropic#2", origin, via)
	}
	h.Set(keyHeader, object.AnthropicKeyList+"#3")
	if origin, via := openrouterFam.reached(prov, nil, servingOf(h)); origin != "api.maker.example#6" || via != "anthropic#6" {
		t.Errorf("a key of the list came from %q via %q, want place 6: after the three names", origin, via)
	}
	if origin, via := openrouterFam.reached(prov, nil, servingOf(http.Header{})); origin != "reseller.example" || via != "" {
		t.Errorf("the reseller's answer came from %q via %q, want its own host and no account", origin, via)
	}
	if origin, via := zenFam.reached(prov, nil, servingOf(h)); via != "" || origin != "reseller.example" {
		t.Errorf("a family with no maker read %q via %q from a maker's headers", origin, via)
	}

	// Each key that refused first is an arm of the chain, read back as the family
	// service's are.
	missed := []miss{
		{upstream: claudeID, provider: "anthropic", reason: "key 1: 401 authentication_error"},
		{upstream: claudeID, provider: "anthropic", reason: "key 2: 529 overloaded_error"},
	}
	if got := arms(chain(missed)); !slices.Equal(got, missed) {
		t.Errorf("the chain read back as %+v, want %+v", got, missed)
	}
	rows := failoverRows(whence{ledger: "acme", ctx: context.Background()}, openrouterFam, claudeSKU, chain(missed), &iam.User{Owner: "acme", Name: "alice"}, true, false, "req-1")
	if len(rows) != 2 || rows[0].ErrorMsg != claudeID+": key 1: 401 authentication_error" || rows[0].Vendor != "anthropic" || rows[0].Status != "failover" {
		t.Errorf("failover rows = %+v", rows)
	}
}

// The pool is the reserved names in order, then each line of the list; a key's
// place is its name's whatever the names before it hold, and one key held twice
// is one account.
func TestTheMakersPoolIsItsNamesThenItsList(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY_2", "second")
	t.Setenv("ANTHROPIC_API_KEY_3", "third")
	t.Setenv(object.AnthropicKeyList, "fourth\n\n  third  \nfifth\n")
	var got []string
	for _, a := range anthropicNative.accounts() {
		got = append(got, fmt.Sprintf("%d %s %s", a.Place, a.Name, a.Key))
	}
	want := []string{"2 ANTHROPIC_API_KEY_2 second", "3 ANTHROPIC_API_KEY_3 third", "4 ANTHROPIC_API_KEYS#1 fourth", "6 ANTHROPIC_API_KEYS#3 fifth"}
	if !slices.Equal(got, want) {
		t.Fatalf("accounts = %q, want %q", got, want)
	}
	for _, a := range anthropicNative.accounts() {
		if p := anthropicNative.place(a.Name); p != a.Place {
			t.Errorf("place(%s) = %d, want %d", a.Name, p, a.Place)
		}
	}
	// With no address the maker is never asked, whatever keys are held.
	t.Setenv("ANTHROPIC_URL", "")
	if resp, missed := anthropicNative.send(context.Background(), nativeCall{path: "chat/completions", sku: claudeSKU, body: []byte(hello(claudeSKU)), start: time.Now()}); resp != nil || missed != nil {
		t.Fatal("a maker with no address was asked")
	}
}
