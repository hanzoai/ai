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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// gptSKU is an OpenAI model as OpenRouter lists it, and gptID what OpenAI calls it.
const (
	gptSKU = "openai/gpt-5.4-mini"
	gptID  = "gpt-5.4-mini"
)

// chatMaker is a vendor that speaks OpenAI's Chat Completions on httptest: a list
// of models for each key, and a chat endpoint that answers each key as the case
// says and remembers what it was sent.
type chatMaker struct {
	*httptest.Server

	mu     sync.Mutex
	refuse map[string]chatRefused // bearer key → what it is answered with; absent = an answer
	asked  []string
	bodies []string
}

type chatRefused struct {
	status int
	code   string
}

// gptEvents is a stream that says "ok", ending with its usage: 6,000 prompt
// tokens of which 3,000 were read from the cache, and 500 written out.
var gptEvents = []string{
	`{"id":"chatcmpl-v","object":"chat.completion.chunk","model":"gpt-5.4-mini-2026-03-17","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
	`{"id":"chatcmpl-v","object":"chat.completion.chunk","model":"gpt-5.4-mini-2026-03-17","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
	`{"id":"chatcmpl-v","object":"chat.completion.chunk","model":"gpt-5.4-mini-2026-03-17","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	`{"id":"chatcmpl-v","object":"chat.completion.chunk","model":"gpt-5.4-mini-2026-03-17","choices":[],"usage":{"prompt_tokens":6000,"completion_tokens":500,"total_tokens":6500,"prompt_tokens_details":{"cached_tokens":3000}}}`,
}

func (m *chatMaker) serve(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"`+gptID+`","object":"model"},{"id":"gpt-5.4-mini-2026-03-17","object":"model"}]}`)
		return
	}
	b, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.asked = append(m.asked, key)
	m.bodies = append(m.bodies, string(b))
	no, refused := m.refuse[key]
	m.mu.Unlock()
	if refused {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(no.status)
		fmt.Fprintf(w, `{"error":{"message":"refused","type":"invalid_request_error","param":null,"code":%q}}`, no.code)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range gptEvents {
		fmt.Fprintf(w, "data: %s\n\n", e)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (m *chatMaker) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.asked)
}

// chatMakerAt starts a chat maker, makes it OpenAI's address with keys under the
// reserved names in order, and reads what each key lists.
func chatMakerAt(t *testing.T, m *chatMaker, keys ...string) *chatMaker {
	t.Helper()
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	t.Setenv("OPENAI_URL", m.URL)
	for i, name := range object.OpenAIKeys {
		key := ""
		if i < len(keys) {
			key = keys[i]
		}
		t.Setenv(name, key)
	}
	t.Setenv(object.OpenAIKeyList, "")
	forgetKeys()
	cooled.forget()
	openaiNative.forget()
	t.Cleanup(func() {
		forgetKeys()
		cooled.forget()
		openaiNative.forget()
	})
	openaiNative.refresh()
	return m
}

// gptResale lists the OpenAI SKU on the resale family at the vendor's price times
// OpenRouter's fee: $0.75 in, $4.50 out, $0.075 a cached read a million tokens.
func gptResale(t *testing.T, or *accounts) *modelFamily {
	t.Helper()
	fam := resale(t, or)
	tier := zenTier{
		MaxCtx: 400_000,
		In:     decimal.MustParse("0.79125"), Out: decimal.MustParse("4.7475"),
		CacheRead: decimal.MustParse("0.079125"),
	}
	fam.byID[gptSKU] = zenModel{ID: gptSKU, Margin: decimal.MustParse("1.055"), Base: tier, Tiers: []zenTier{tier}}
	fam.ids = append(fam.ids, gptSKU)
	return fam
}

// An OpenAI model is asked of OpenAI before the reseller that lists it, under
// OpenAI's own id, with the ceiling OpenAI reads and the usage chunk it is asked
// for; the reseller is never asked once it answers, and the call bills what it
// cost at OpenAI's price with the platform's fee on top.
func TestAnOpenAIModelIsAskedOfItsMakerFirst(t *testing.T) {
	or := &accounts{}
	fam := gptResale(t, or)
	m := chatMakerAt(t, &chatMaker{}, "o1")
	got := ledger(t)
	alice := &iam.User{Owner: "acme", Name: "alice"}

	body := `{"model":"` + gptSKU + `","max_tokens":64,"messages":[{"role":"user","content":"2+2?"}]}`
	for _, stream := range []bool{true, false} {
		c := visit(http.MethodPost, "/v1/chat/completions")
		out, answer := chat(t, c, fam, gptSKU, body, stream, alice)
		if out != nil || !strings.Contains(answer, "ok") {
			t.Fatalf("stream=%v: attempts=%+v answer=%s", stream, out, answer)
		}
		if strings.Contains(answer, "gpt-5.4-mini-2026-03-17") || !strings.Contains(answer, gptSKU) {
			t.Errorf("stream=%v: answer %s should wear the model asked for, never the vendor's id", stream, answer)
		}
	}
	if calls := m.calls(); !slices.Equal(calls, []string{"o1", "o1"}) {
		t.Fatalf("the maker was asked %v, want twice on its one key", calls)
	}
	if calls := or.calls(); len(calls) != 0 {
		t.Fatalf("the reseller was asked %v after the maker answered", calls)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal([]byte(m.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if string(sent["model"]) != `"`+gptID+`"` || string(sent["max_completion_tokens"]) != "64" || sent["max_tokens"] != nil ||
		string(sent["stream"]) != "true" || !strings.Contains(string(sent["stream_options"]), `"include_usage":true`) {
		t.Errorf("the maker was sent %s, want OpenAI's id, max_completion_tokens 64, a stream and its usage", m.bodies[0])
	}

	// 3,000 fresh, 3,000 cached, 500 out at OpenAI's price:
	//   (3000×0.75 + 3000×0.075 + 500×4.50) / 1e6 = $0.004725
	//   times the platform's 5.5%                  = $0.004984875
	const cost, price = "0.004725", "0.004984875"
	ev := got()
	if len(ev) != 2 {
		t.Fatalf("%d debits, want one an answer: %+v", len(ev), ev)
	}
	for i, e := range ev {
		if e.USD != price || e.CostUSD != cost || e.Model != gptSKU {
			t.Errorf("debit %d = $%s at a cost of $%s for %s, want $%s at $%s for %s", i, e.USD, e.CostUSD, e.Model, price, cost, gptSKU)
		}
	}
}

// An OpenAI account out of credit (429 insufficient_quota) sits out, is said once
// on the host's bus by its name, and the reseller answers the same request; a
// refusal of the request itself benches nothing and goes to the reseller too.
func TestAnOpenAIAccountOutOfCreditSitsOutAndTheResellerAnswers(t *testing.T) {
	or := &accounts{}
	fam := gptResale(t, or)
	m := chatMakerAt(t, &chatMaker{refuse: map[string]chatRefused{
		"o1": {status: http.StatusTooManyRequests, code: "insufficient_quota"},
	}}, "o1", "o2")

	var heard []object.Drained
	var mu sync.Mutex
	object.SetDrainedPublisher(func(_ context.Context, d object.Drained) {
		mu.Lock()
		heard = append(heard, d)
		mu.Unlock()
	})
	t.Cleanup(func() { object.SetDrainedPublisher(nil) })
	drainedMu.Lock()
	drainedSaid = map[string]time.Time{}
	drainedMu.Unlock()

	if out, answer := chat(t, visit(http.MethodPost, "/v1/chat/completions"), fam, gptSKU, hello(gptSKU), false, nil); out != nil || !strings.Contains(answer, "ok") {
		t.Fatalf("attempts=%+v answer=%s", out, answer)
	}
	if calls := m.calls(); !slices.Equal(calls, []string{"o1", "o2"}) {
		t.Fatalf("the maker was asked %v, want the spent key and then the next", calls)
	}
	accts := openaiNative.accounts()
	if !cooling(accts[0].Key, false) || cooling(accts[1].Key, false) {
		t.Errorf("after insufficient_quota and an answer the keys sit out %v %v, want true false",
			cooling(accts[0].Key, false), cooling(accts[1].Key, false))
	}
	mu.Lock()
	said := slices.Clone(heard)
	mu.Unlock()
	if len(said) != 1 || said[0].Vendor != "openai" || said[0].Account != "OPENAI_API_KEY" || said[0].Status != http.StatusPaymentRequired {
		t.Errorf("the bus heard %+v, want openai's OPENAI_API_KEY drained once", said)
	}

	// Every key spent: the reseller answers for the same model.
	m.mu.Lock()
	m.asked, m.refuse = nil, map[string]chatRefused{"o2": {status: http.StatusPaymentRequired, code: "billing_hard_limit_reached"}}
	m.mu.Unlock()
	or.reset()
	if out, answer := chat(t, visit(http.MethodPost, "/v1/chat/completions"), fam, gptSKU, hello(gptSKU), false, nil); out != nil || !strings.Contains(answer, "ok") {
		t.Fatalf("attempts=%+v answer=%s", out, answer)
	}
	if calls, sold := m.calls(), or.calls(); !slices.Equal(calls, []string{"o2"}) || !slices.Equal(sold, []string{"k1"}) {
		t.Fatalf("the maker was asked %v and the reseller %v, want the one key left and then the reseller", calls, sold)
	}
	if b := decoded(t, []byte(or.bodies[0])); string(b["model"]) != `"`+gptSKU+`"` {
		t.Errorf("the reseller was asked for %s, want the model named", b["model"])
	}

	// The request's own refusal benches nothing.
	forgetKeys()
	m.mu.Lock()
	m.asked, m.refuse = nil, map[string]chatRefused{"o1": {status: http.StatusBadRequest, code: "unsupported_parameter"}}
	m.mu.Unlock()
	or.reset()
	if out, answer := chat(t, visit(http.MethodPost, "/v1/chat/completions"), fam, gptSKU, hello(gptSKU), false, nil); out != nil || !strings.Contains(answer, "ok") {
		t.Fatalf("attempts=%+v answer=%s", out, answer)
	}
	if calls, sold := m.calls(), or.calls(); !slices.Equal(calls, []string{"o1"}) || !slices.Equal(sold, []string{"k1"}) {
		t.Fatalf("a refused request: the maker was asked %v and the reseller %v, want one key and then the reseller", calls, sold)
	}
	if cooling(accts[0].Key, false) {
		t.Error("a key sits out for a refusal of the request")
	}
}

// OpenAI keeps the dots of a model's version in its id; Anthropic writes them as
// dashes. Each maker is asked only for the SKUs its prefix names.
func TestEachMakerIsAskedForItsOwnModelsUnderItsOwnSpelling(t *testing.T) {
	for _, c := range []struct {
		sku   string
		maker *native
		id    string
	}{
		{"openai/gpt-5.4-mini", openaiNative, "gpt-5.4-mini"},
		{"anthropic/claude-haiku-5.5", anthropicNative, "claude-haiku-5-5"},
		{"openai/gpt-5-mini:batch", nil, ""},
		{"google/gemini-3-pro-image", nil, ""},
	} {
		n := openrouterFam.maker(c.sku)
		if n != c.maker {
			t.Errorf("%s: maker %v, want %v", c.sku, n, c.maker)
			continue
		}
		if n == nil {
			continue
		}
		if id, ok := n.spelled(c.sku); !ok || id != c.id {
			t.Errorf("%s: spelled %q %v, want %q", c.sku, id, ok, c.id)
		}
	}
}
