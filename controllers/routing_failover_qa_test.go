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

// routing_failover_qa_test.go — the QA suite for issue #137: model routing and
// failover, end to end through the relay.
//
// The pieces are unit-tested apart (fault.go classifies statuses, retry.go waits a
// vendor out, forward_test.go moves one request to one fallback). What was never
// proven together is what the issue asks about: that every id advertised by
// /v1/models reaches the backend its row names, that a fallback receives the
// whole conversation and not a paraphrase, and that no failure mode — an HTML
// error page behind a 200, a stream that never began, a vendor hanging up, nobody
// answering — ever reaches the caller as a silent, billed success.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ── the catalog: every advertised id routes to the backend its row names ────

func TestEveryAdvertisedModelRoutesToTheBackendItsRowNames(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "conf", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var file ModelConfigFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(filepath.Join("..", "conf", "models.yaml")); err != nil {
		t.Fatal(err)
	}

	folded := map[string]string{}
	for name := range file.Models {
		key := strings.ToLower(name)
		if other, dup := folded[key]; dup {
			t.Errorf("%q and %q fold to one id: one of them is served as the other without saying so", other, name)
		}
		folded[key] = name
	}

	for name, def := range file.Models {
		if def.AliasOf != "" || def.PricingOnly {
			continue
		}
		r := mc.ResolveRoute(name)
		if r == nil {
			t.Errorf("%q is neither an alias nor pricing-only, yet nothing resolves it", name)
			continue
		}
		if r.providerName != def.Provider || r.upstreamModel != def.Upstream {
			t.Errorf("%q resolves to %s/%s, not the row's %s/%s — a silent substitution",
				name, r.providerName, r.upstreamModel, def.Provider, def.Upstream)
		}
		if def.Provider == "" || def.Upstream == "" {
			t.Errorf("%q is advertised but names no backend", name)
		}
		for i, fb := range r.fallbacks {
			if fb.providerName == "" || fb.upstreamModel == "" {
				t.Errorf("%q fallback %d names no backend: %s/%s", name, i, fb.providerName, fb.upstreamModel)
			}
		}
	}

	// An alias must land on a model something routes, and must not sit beside a
	// route of its own name — the request meets the route table first, so such an
	// alias never fires and the id means two things at once.
	for alias, target := range mc.aliases {
		if _, shadow := mc.routes[alias]; shadow {
			t.Errorf("alias %q has a route of the same name: the alias never fires", alias)
		}
		id, hops := target, 0
		for mc.ResolveRoute(id) == nil && hops < 8 {
			next, chained := mc.aliases[strings.ToLower(id)]
			if !chained {
				break
			}
			id, hops = next, hops+1
		}
		if mc.ResolveRoute(id) == nil {
			t.Errorf("alias %q ends at %q, which nothing routes", alias, id)
		}
	}
}

// ── the fallback receives what the caller wrote, and answers in the open ────

func TestAFallbackAnswersTheWholeConversation(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, declines(http.StatusServiceUnavailable, "overloaded"), completes("from b", 30, 0, 5))

	c := w.chat(conversation)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if !strings.Contains(sent(c), "from b") {
		t.Errorf("the fallback did not answer: %s", sent(c))
	}

	// The fallback was offered the whole conversation — every system turn, tool,
	// response format and parameter — not a paraphrase of it.
	if got, want := canonical(t, w.b.sent(t, 0), ourFields...), canonical(t, []byte(conversation), ourFields...); got != want {
		t.Errorf("the fallback was sent a different request than the caller wrote\nfallback: %s", w.b.sent(t, 0))
	}

	// The answer names the SKU the caller bought, never the fallback's own id: an
	// undisclosed substitution is what this suite exists to catch.
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sent(c)), &env); err != nil {
		t.Fatalf("answer is not JSON: %v\n%s", err, sent(c))
	}
	if got := field(t, env, "model"); got != relaySku {
		t.Errorf("a failover answer names %q, want the SKU it served", got)
	}
}

func TestFailoverKeepsTheStreamStreaming(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, declines(http.StatusServiceUnavailable, "overloaded"), streams([]string{"from ", "b"}, 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","stream":true,"temperature":0.5,"messages":[{"role":"user","content":"hi"}]}`)
	out := sent(c)
	if answered(c) != http.StatusOK || !strings.Contains(out, "from ") || !strings.Contains(out, `"content":"b"`) {
		t.Fatalf("the fallback's stream never reached the caller: status %d\n%s", answered(c), out)
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Fatalf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}

	var fields map[string]json.RawMessage
	_ = json.Unmarshal(w.b.sent(t, 0), &fields)
	if string(fields["stream"]) != "true" {
		t.Errorf("the fallback was asked to drop the stream: %s", w.b.sent(t, 0))
	}
	if string(fields["model"]) != `"b-up"` || string(fields["temperature"]) != "0.5" {
		t.Errorf("the fallback was sent %s, want its own id and the caller's temperature", w.b.sent(t, 0))
	}
}

// ── failure modes that must never arrive as a silent, billed success ────────

// A rate limit is the vendor's, not the caller's: once its ride is spent the
// request moves, it does not end. (retry_test covers waiting one vendor out.)
func TestARateLimitedVendorFailsOverWhenItsRideIsDone(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, declines(http.StatusTooManyRequests, "rate limit"), completes("from b", 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK || !strings.Contains(sent(c), "from b") {
		t.Fatalf("a 429 ended the request: status %d: %s", answered(c), sent(c))
	}
	if w.b.asked() != 1 {
		t.Errorf("the fallback was asked %d time(s), want once", w.b.asked())
	}
	if !cooled.cooling(credential{"relayco", "relay-a"}) {
		t.Error("the rate-limited vendor was not rested")
	}
}

func hangsUp(w http.ResponseWriter, _ int) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		_ = conn.Close()
	}
}

// An upstream that dies mid-dial is a provider fault like any other.
func TestAVendorThatHangsUpFailsOverToTheFallback(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, hangsUp, completes("from b", 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK || !strings.Contains(sent(c), "from b") {
		t.Fatalf("a hung-up vendor ended the request: status %d: %s", answered(c), sent(c))
	}
}

// A proxy's error page wearing the vendor's 200 is not an answer. Delivered, it
// would bill the prompt for bytes no model wrote; moved, the fallback answers.
func TestAnHTMLPageBehindA200IsMovedAsARefusal(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "text/html")
		fmt.Fprint(rw, `<html><head><title>502 Bad Gateway</title></head><body>nginx</body></html>`)
	}, completes("from b", 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK || !strings.Contains(sent(c), "from b") {
		t.Fatalf("an HTML error page was served as an answer: status %d: %s", answered(c), sent(c))
	}
	if strings.Contains(sent(c), "<html") {
		t.Errorf("the caller received the proxy's page, not an answer: %s", sent(c))
	}
}

// A stream that ends before any answer frame is a refusal, not an empty 200.
func TestAStreamThatNeverBeganIsNotAnEmptyAnswer(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(rw, `data: {"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up",`+
			`"choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n")
	}, streams([]string{"answered by b"}, 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out := sent(c)
	if answered(c) != http.StatusOK || !strings.Contains(out, "answered by b") {
		t.Fatalf("a stream that never began was served as an answer: status %d\n%s", answered(c), out)
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Errorf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}
}

// When every provider refuses, the caller hears an error — never a 200 with
// nothing in it, and nothing left held against the bill.
func TestNobodyAnsweringIsNotAnEmptyOK(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, declines(http.StatusServiceUnavailable, "down"), declines(http.StatusTooManyRequests, "rate limit"))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) == http.StatusOK {
		t.Fatalf("nobody answered and the caller was told OK: %s", sent(c))
	}
	if !strings.Contains(sent(c), "error") || strings.Contains(sent(c), `"object":"chat.completion"`) {
		t.Errorf("an exhausted route reached the caller as an answer: %s", sent(c))
	}
	if got := w.spent(t); got != 0 {
		t.Errorf("a request nothing answered cost the caller %d cents", got)
	}
}

// A model nothing routes is refused by name. Answering it from some other model
// that happened to be free is the undisclosed substitution this issue names.
func TestAnUnknownModelIsRefusedNotSilentlySubstituted(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, completes("ok", 3, 0, 1), completes("ok", 3, 0, 1))

	c := w.chat(`{"model":"no-such-model-zz","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) < 400 {
		t.Fatalf("status %d, want a refusal: %s", answered(c), sent(c))
	}
	if w.a.asked() != 0 || w.b.asked() != 0 {
		t.Errorf("an unknown model was answered by a vendor anyway: asked a=%d b=%d", w.a.asked(), w.b.asked())
	}
}

// The one empty answer that is honest: the model itself said "" with its
// finish_reason attached. It travels as what it is — stamped to the SKU, billed
// for what it used — and no fallback is asked over it. Anything less honest than
// this (an error page, an empty end) is refused above.
func TestAnEmptyAnswerIsRelayedAsTheModelGaveIt(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "application/json")
		fmt.Fprint(rw, `{"id":"up-1","object":"chat.completion","created":1,"model":"a-up",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":0,"total_tokens":3}}`)
	}, never(t))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	var env struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(sent(c)), &env); err != nil {
		t.Fatal(err)
	}
	if env.Model != relaySku {
		t.Errorf("the empty answer names %q, want the SKU it is billed as", env.Model)
	}
	if len(env.Choices) != 1 || env.Choices[0].Message.Content != "" {
		t.Errorf("the model's empty answer was not relayed as given: %s", sent(c))
	}
}
