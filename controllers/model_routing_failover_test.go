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
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Issue #137: model routing & failover, driven end to end. The catalogue is asked
// through the real ListModels handler; every routing behaviour is asked through the
// real ChatCompletions handler over newRelayWorld's scripted upstreams — a primary
// and a fallback, a real store, a funded caller. No test reaches a live vendor.

// TestModelsCatalogIsTheAdvertisedRoutingTable: GET /v1/models answers 200 with the
// list envelope, and every row it advertises is a model the router can actually
// route — a listing that names an unroutable id sends callers at a 404.
func TestModelsCatalogIsTheAdvertisedRoutingTable(t *testing.T) {
	probes := []string{"routing-probe-a", "routing-probe-b"}
	withCatalog(t, probes...)

	c := visit(http.MethodGet, "/v1/models")
	if status := answering(t, c, c.ListModels); status != http.StatusOK {
		t.Fatalf("GET /v1/models = %d: %s", status, sent(c))
	}
	if ct := string(c.Fiber().Response().Header.ContentType()); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var out modelList
	if err := json.Unmarshal([]byte(sent(c)), &out); err != nil {
		t.Fatalf("the listing is not JSON: %v\n%s", err, sent(c))
	}
	if out.Object != "list" {
		t.Errorf("object = %q, want list", out.Object)
	}
	if out.Models == nil || len(out.Models) != 0 {
		t.Errorf("models = %v, want the empty compatibility array", out.Models)
	}
	if len(out.Data) != len(probes) {
		t.Fatalf("data holds %d rows, want exactly the configured %v: %s", len(out.Data), probes, sent(c))
	}
	for i, m := range out.Data {
		if m.ID != probes[i] {
			t.Errorf("row %d = %q, want %q — the listing is sorted by id", i, m.ID, probes[i])
		}
		if m.Object != "model" || m.Created <= 0 || m.OwnedBy == "" {
			t.Errorf("row %q is not a model row: %+v", m.ID, m)
		}
		route := GetModelConfig().ResolveRoute(m.ID)
		if route == nil || route.providerName != "do-ai" {
			t.Errorf("listed %q resolves to %+v — an advertised model must be routable", m.ID, route)
		}
	}
}

// headerTimeout bounds the relay's wait for response headers. The bound the router
// keeps is on the headers, not the whole request — a whole-request timeout cuts a
// streamed answer mid-sentence — so this is the seam a slow provider is felt through.
func headerTimeout(t *testing.T, wait time.Duration) {
	t.Helper()
	prev := zenPipeClient
	zenPipeClient = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: wait}}
	t.Cleanup(func() { zenPipeClient = prev })
}

// slowToAnswer is a provider that answers, but later than the router waits.
func slowToAnswer(after time.Duration) func(http.ResponseWriter, int) {
	return func(w http.ResponseWriter, n int) {
		time.Sleep(after)
		completes("too late", 3, 0, 1)(w, n)
	}
}

// A provider that never answers inside the header bound is a provider fault: the
// request moves to the fallback, and the answer the router stopped waiting for never
// reaches the caller.
func TestProviderTimeoutFailsOverToTheBackup(t *testing.T) {
	quick(t)
	headerTimeout(t, 100*time.Millisecond)
	w := newRelayWorld(t, slowToAnswer(500*time.Millisecond), completes("answered after the timeout", 3, 0, 2))

	start := time.Now()
	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if !strings.Contains(sent(c), "answered after the timeout") {
		t.Errorf("answer = %s, want the backup's", sent(c))
	}
	if strings.Contains(sent(c), "too late") {
		t.Errorf("the answer the router stopped waiting for reached the caller: %s", sent(c))
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Errorf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("a hung provider held the request %s; the header bound is 100ms", took)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sent(c)), &env); err != nil {
		t.Fatalf("answer is not JSON: %v\n%s", err, sent(c))
	}
	if got := field(t, env, "model"); got != relaySku {
		t.Errorf("model = %q, want the SKU the caller asked for", got)
	}
	if !cooled.cooling(credential{"relayco", "relay-a"}) {
		t.Error("the provider that timed out was not rested")
	}
}

// With every provider past the header bound the caller is told 503 in bounded time —
// an error, not a hang, and not a partial answer.
func TestTimeoutsWithNobodyLeftAreAnErrorNotAHang(t *testing.T) {
	quick(t)
	headerTimeout(t, 100*time.Millisecond)
	w := newRelayWorld(t, slowToAnswer(500*time.Millisecond), slowToAnswer(500*time.Millisecond))

	start := time.Now()
	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 — every provider timed out: %s", answered(c), sent(c))
	}
	if !strings.Contains(sent(c), "unavailable") {
		t.Errorf("body %s does not say the model is unavailable", sent(c))
	}
	if strings.Contains(sent(c), `"choices"`) {
		t.Errorf("a refusal carried an answer: %s", sent(c))
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Errorf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("two hung providers held the request %s", took)
	}
	if got := w.spent(t); got != 0 {
		t.Errorf("a request no provider answered cost the caller %d cents", got)
	}
}

// A 429 is waited out on the same provider first (the retry policy), and only a
// provider still refusing is given up on. The refusal is counted once per provider —
// the retries happen inside the one attempt — under the reason an operator acts on,
// and the answer is billed to the provider that actually served it.
func TestRateLimitIsRiddenOutThenFailsOverAndRecorded(t *testing.T) {
	quick(t)
	defaultRetryPolicy = retryPolicy{attempts: 2, base: time.Millisecond, max: time.Millisecond}
	const series = `cloud_supply_refused{provider="relay-a",reason="refused"}`
	was := reading(t, scrape(t), series)

	w := newRelayWorld(t, declines(http.StatusTooManyRequests, "Rate limit exceeded for requests, please try again later."),
		completes("past the rate limit", 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK || !strings.Contains(sent(c), "past the rate limit") {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if w.a.asked() != 2 {
		t.Errorf("the busy provider was asked %d time(s), want the policy's 2", w.a.asked())
	}
	if w.b.asked() != 1 {
		t.Errorf("the fallback was asked %d time(s), want 1", w.b.asked())
	}
	if got := reading(t, scrape(t), series) - was; got != 1 {
		t.Errorf("%s moved by %v, want 1 — a rate-limited provider must be visible to a collector, once per attempt", series, got)
	}
	if !cooled.cooling(credential{"relayco", "relay-a"}) {
		t.Error("the rate-limited provider was not rested")
	}
	debit := w.paid(t)
	if debit.Provider != "relay-b" || debit.Model != relaySku {
		t.Errorf("billed provider=%q model=%q, want the serving relay-b under the SKU", debit.Provider, debit.Model)
	}
}

// Every provider rate-limited is an honest 503: the caller is told the model could
// not answer, not which of our vendors are busy or what they answered — a vendor's
// 429 is not the caller's, and forwarding it sends them to retry a limit they never
// hit. Both refusals reach the operator's counter, and the caller owes nothing.
func TestEveryProviderRateLimitedIsAnHonest503(t *testing.T) {
	quick(t)
	const (
		seriesA = `cloud_supply_refused{provider="relay-a",reason="refused"}`
		seriesB = `cloud_supply_refused{provider="relay-b",reason="refused"}`
	)
	was := scrape(t)
	wasA, wasB := reading(t, was, seriesA), reading(t, was, seriesB)

	w := newRelayWorld(t, declines(http.StatusTooManyRequests, "rate limit exceeded"),
		declines(http.StatusTooManyRequests, "rate limit exceeded"))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", answered(c), sent(c))
	}
	if !strings.Contains(sent(c), relaySku) || !strings.Contains(sent(c), "unavailable") {
		t.Errorf("body %s does not name the model and say it is unavailable", sent(c))
	}
	for _, leak := range []string{"relay-a", "relay-b", "429"} {
		if strings.Contains(sent(c), leak) {
			t.Errorf("the refusal leaks %q to the caller: %s", leak, sent(c))
		}
	}
	if strings.Contains(sent(c), `"choices"`) {
		t.Errorf("a refusal carried an answer: %s", sent(c))
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Errorf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}
	now := scrape(t)
	if got := reading(t, now, seriesA) - wasA; got != 1 {
		t.Errorf("%s moved by %v, want 1", seriesA, got)
	}
	if got := reading(t, now, seriesB) - wasB; got != 1 {
		t.Errorf("%s moved by %v, want 1", seriesB, got)
	}
	if got := w.spent(t); got != 0 {
		t.Errorf("a request no provider answered cost the caller %d cents", got)
	}
}

// The most important property: a primary answering 500 is unavailable, and the
// fallback serves the SAME request — every parameter the caller wrote arrives under
// the fallback's own model id, the answer is stamped the SKU, and only the fallback's
// answer is billed.
func TestPrimary500FailsOverWithTheRequestIntact(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, declines(http.StatusInternalServerError, "internal server error"),
		completes("from the backup", 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"system","content":"You are terse."},{"role":"user","content":"hi"}],` +
		`"temperature":0,"top_p":0.5,"max_tokens":256,"stop":["END"],` +
		`"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}}`)
	if answered(c) != http.StatusOK || !strings.Contains(sent(c), "from the backup") {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Fatalf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(w.b.sent(t, 0), &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"model": `"b-up"`, "temperature": "0", "top_p": "0.5", "max_tokens": "256", "stop": `["END"]`,
	} {
		if string(fields[key]) != want {
			t.Errorf("the fallback was sent %s = %s, want the caller's %s", key, fields[key], want)
		}
	}
	var format struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(fields["response_format"], &format); err != nil || format.Type != "json_schema" {
		t.Errorf("response_format = %s, want the caller's json_schema", fields["response_format"])
	}
	var msgs []map[string]any
	if err := json.Unmarshal(fields["messages"], &msgs); err != nil || len(msgs) != 2 {
		t.Fatalf("messages = %s, want both turns", fields["messages"])
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "You are terse." {
		t.Errorf("the system prompt did not survive the move: %s", fields["messages"])
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sent(c)), &env); err != nil {
		t.Fatal(err)
	}
	if got := field(t, env, "model"); got != relaySku {
		t.Errorf("model = %q, want the SKU — the caller never learns which vendor served", got)
	}
	if !cooled.cooling(credential{"relayco", "relay-a"}) {
		t.Error("the provider that answered 500 was not rested")
	}
	debit := w.paid(t)
	if debit.Provider != "relay-b" || debit.Model != relaySku {
		t.Errorf("billed provider=%q model=%q, want relay-b under the SKU", debit.Provider, debit.Model)
	}
}

// A vendor that vanishes without a status and without a byte — the connection cut
// before any answer — is unavailable like any 5xx: nothing was written, so the
// request is still movable, and the fallback serves it.
func TestAConnectionCutBeforeAnyAnswerFailsOver(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		if conn, _, err := rw.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}, completes("the backup answered", 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) != http.StatusOK || !strings.Contains(sent(c), "the backup answered") {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Errorf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}
	if !cooled.cooling(credential{"relayco", "relay-a"}) {
		t.Error("the provider that cut the connection was not rested")
	}
}

// A stream is judged by its opening frames: a vendor that refuses before the first
// byte has written nothing, so the stream moves to the fallback and the caller reads
// one whole answer — never the refusal spliced onto it.
func TestStreamFailsOverBeforeItsFirstByte(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, declines(http.StatusServiceUnavailable, "down before the first byte"),
		streams([]string{"streamed ", "from the backup"}, 3, 0, 2))

	c := w.chat(`{"model":"relay-sku","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out := sent(c)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), out)
	}
	for _, part := range []string{`"content":"streamed "`, `"content":"from the backup"`, `"finish_reason":"stop"`, "[DONE]"} {
		if !strings.Contains(out, part) {
			t.Errorf("the streamed answer is missing %s:\n%s", part, out)
		}
	}
	if strings.Contains(out, "down before the first byte") {
		t.Errorf("the refusal reached the caller's stream:\n%s", out)
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Errorf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}
}