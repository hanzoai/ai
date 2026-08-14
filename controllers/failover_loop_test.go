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

package controllers

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// fleet scripts a set of providers: each name answers with an error, or serves.
// It records the order it was asked in, which is the property most of these
// tests are really about.
type fleet struct {
	answer map[string]error // nil entry ⇒ that provider serves
	asked  []string
	// wrote is text a provider emits into the writer before failing, to prove a
	// dead provider's half-sentence never reaches the next one's answer.
	wrote map[string]string
}

func (f *fleet) install(t *testing.T) {
	t.Helper()
	saved := callProvider
	t.Cleanup(func() { callProvider = saved })
	callProvider = func(org string, row *object.Provider, c candidate, question string,
		w io.Writer, history, knowledge []*model.RawMessage, lang string,
	) (*model.ModelResult, *object.Provider, error) {
		f.asked = append(f.asked, c.provider)
		if s := f.wrote[c.provider]; s != "" {
			_, _ = w.Write([]byte(s))
		}
		if err, ok := f.answer[c.provider]; ok && err != nil {
			return nil, &object.Provider{Owner: "admin", Name: c.provider}, err
		}
		return &model.ModelResult{ResponseTokenCount: 7},
			&object.Provider{Owner: "admin", Name: c.provider}, nil
	}
}

// buffer is the minimum a writer must be for the loop: it accumulates, and it
// can be told to forget. Mirrors OpenAIWriter/AnthropicWriter, which both
// APPEND on every Write.
type buffer struct {
	text string
	sent bool // set once bytes have reached the "client"
}

func (b *buffer) Write(p []byte) (int, error) { b.text += string(p); return len(p), nil }
func (b *buffer) Reset()                      { b.text = "" }

func quick(t *testing.T) {
	t.Helper()
	savedPolicy, savedConfig := defaultRetryPolicy, globalModelConfig
	t.Cleanup(func() { defaultRetryPolicy, globalModelConfig = savedPolicy, savedConfig })
	// One try per provider and no sleeping: these tests are about which vendor
	// is asked, not about how patiently each one is waited on (retry_test covers
	// that). The config must be cleared as well as the default, because
	// currentRetryPolicy reads models.yaml FIRST — a config another test in this
	// package left loaded would otherwise reinstate the real backoff.
	globalModelConfig = nil
	defaultRetryPolicy = retryPolicy{attempts: 1, base: time.Millisecond, max: time.Millisecond}
}

// distinct counts the VENDORS asked, which is what the cap bounds. The same
// vendor asked twice by the same-provider retry is one vendor.
func distinct(asked []string) int {
	seen := map[string]bool{}
	for _, a := range asked {
		seen[a] = true
	}
	return len(seen)
}

func newAsk(r *modelRoute, w io.Writer, sent func() bool) ask {
	return ask{route: r, model: "test-model", question: "hi", writer: w, sent: sent}
}

// THE OUTAGE. One vendor's account is empty; the product must not go dark.
func TestPaymentRequiredFailsOverAndTheNextProviderServes(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{
		"enso": apiErr(402, "Insufficient credits."),
	}}
	f.install(t)

	var w buffer
	res, by, tried, err := newAsk(route("enso", "do-ai"), &w, nil).serve()
	if err != nil {
		t.Fatalf("a 402 from one vendor must not fail the request: %v", err)
	}
	if res == nil || by.name != "do-ai" {
		t.Fatalf("served by %q, want do-ai", by.name)
	}
	if strings.Join(f.asked, ",") != "enso,do-ai" {
		t.Errorf("asked %v, want [enso do-ai]", f.asked)
	}
	if len(tried) != 1 || tried[0].provider != "enso" || tried[0].status != 402 {
		t.Fatalf("the refusal must be reported so the empty account gets noticed, got %+v", tried)
	}
	if tried[0].fault != faultProvider {
		t.Errorf("a 402 is the vendor's fault, got %s", tried[0].fault)
	}
}

// The opposite guard: a broken request must cost exactly one upstream call.
func TestBadRequestDoesNotFailOver(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{
		"do-ai": apiErr(400, "invalid 'messages': empty"),
	}}
	f.install(t)

	var w buffer
	_, _, tried, err := newAsk(route("do-ai", "anthropic", "fireworks"), &w, nil).serve()
	if err == nil {
		t.Fatal("a 400 must surface, not be papered over")
	}
	if len(f.asked) != 1 {
		t.Errorf("asked %v — a malformed request fails identically everywhere; retrying it "+
			"turns one error into %d and bills for each", f.asked, len(f.asked))
	}
	if !strings.Contains(err.Error(), "invalid 'messages'") {
		t.Errorf("the caller must get the REAL reason, got %v", err)
	}
	if len(tried) != 1 || tried[0].fault != faultRequest {
		t.Errorf("want one request-fault attempt, got %+v", tried)
	}
}

// A dead credential must break loudly rather than drain traffic onto whichever
// vendor still works while nobody notices the bill.
func TestUnauthorizedDoesNotFailOver(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{"do-ai": apiErr(401, "invalid api key")}}
	f.install(t)

	var w buffer
	_, _, _, err := newAsk(route("do-ai", "anthropic"), &w, nil).serve()
	if err == nil {
		t.Fatal("a 401 must surface")
	}
	if len(f.asked) != 1 {
		t.Errorf("asked %v — a bad key of ours must not be hidden behind somebody else's", f.asked)
	}
	if cooled.cooling("do-ai") {
		t.Error("a 401 is our fault, not the vendor's — demoting it would blame the wrong party")
	}
}

// The point of demotion: the second request must not pay the dead vendor's
// round trip all over again.
func TestDemotedProviderIsSkippedWhileCoolingDown(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{"enso": apiErr(402, "Insufficient credits.")}}
	f.install(t)

	var w1 buffer
	if _, _, _, err := newAsk(route("enso", "do-ai"), &w1, nil).serve(); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if !cooled.cooling("enso") {
		t.Fatal("a vendor that answered 402 must be demoted")
	}

	f.asked = nil
	var w2 buffer
	_, by, _, err := newAsk(route("enso", "do-ai"), &w2, nil).serve()
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if by.name != "do-ai" {
		t.Fatalf("served by %q, want do-ai", by.name)
	}
	if strings.Join(f.asked, ",") != "do-ai" {
		t.Errorf("asked %v, want [do-ai] only — without the cooldown every single request "+
			"pays the empty vendor's latency before getting a real answer", f.asked)
	}
}

// Demotion is a preference, never a veto: a model whose only vendor is resting
// must still be attempted rather than refused.
func TestCoolingProviderIsStillTriedWhenItIsTheOnlyOne(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	cooled.demote("do-ai", time.Minute)
	f := &fleet{}
	f.install(t)

	var w buffer
	_, by, _, err := newAsk(route("do-ai"), &w, nil).serve()
	if err != nil {
		t.Fatalf("a resting vendor that is the only one must still be asked: %v", err)
	}
	if by.name != "do-ai" || len(f.asked) != 1 {
		t.Errorf("asked %v, served by %q", f.asked, by.name)
	}
}

// When everyone refuses the client gets one honest error naming who was asked
// and why the last one said no.
func TestAllRefusedYieldsAnHonestError(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{
		"enso":      apiErr(402, "Insufficient credits."),
		"do-ai":     apiErr(429, "Platform overloaded"),
		"fireworks": apiErr(503, "upstream is down for maintenance"),
	}}
	f.install(t)

	var w buffer
	_, _, tried, err := newAsk(route("enso", "do-ai", "fireworks"), &w, nil).serve()
	if err == nil {
		t.Fatal("every vendor refused; that must be an error, not an empty answer")
	}
	if len(tried) != 3 {
		t.Fatalf("want 3 recorded refusals, got %d", len(tried))
	}
	msg := err.Error()
	for _, want := range []string{"enso", "402", "do-ai", "429", "fireworks", "503", "maintenance"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q omits %q — the last reason is the one that tells an operator what to fix", msg, want)
		}
	}
	if got := statusOf(err); got != 503 {
		t.Errorf("status = %d, want 503", got)
	}
}

// The cap is what keeps a generous models.yaml from turning a bad afternoon into
// a minute-long request.
func TestAttemptCapHolds(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{
		"p1": apiErr(503, "down"), "p2": apiErr(503, "down"), "p3": apiErr(503, "down"),
		"p4": apiErr(503, "down"), "p5": apiErr(503, "down"),
	}}
	f.install(t)

	var w buffer
	_, _, tried, err := newAsk(route("p1", "p2", "p3", "p4", "p5"), &w, nil).serve()
	if err == nil {
		t.Fatal("everyone was down; want an error")
	}
	if n := distinct(f.asked); n != maxProviders {
		t.Errorf("asked %d vendors (%v), want %d — the walk must be bounded by the constant, "+
			"not by however many fallbacks an operator typed", n, f.asked, maxProviders)
	}
	if len(tried) != maxProviders {
		t.Errorf("recorded %d attempts, want %d", len(tried), maxProviders)
	}
}

// A vendor that already refused elsewhere in this request (the family pipe) is
// not asked again, and it spends one of the three.
func TestPriorRefusalsAreCarried(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{}
	f.install(t)

	prior := []attempt{{provider: "enso", status: 402, fault: faultProvider,
		err: errors.New("Insufficient credits.")}}

	var w buffer
	a := newAsk(route("enso", "do-ai"), &w, nil)
	a.prior = prior
	_, by, tried, err := a.serve()
	if err != nil {
		t.Fatalf("the alternate must serve: %v", err)
	}
	if by.name != "do-ai" || strings.Join(f.asked, ",") != "do-ai" {
		t.Errorf("asked %v, served by %q — the family already refused; asking it again is waste", f.asked, by.name)
	}
	if len(tried) != 1 || tried[0].provider != "enso" {
		t.Errorf("the earlier refusal must travel with the result so it gets recorded, got %+v", tried)
	}
}

// A family model with no alternate declared: the honest answer names the vendor
// and the reason rather than forwarding a 402 that blames the customer.
func TestFamilyRefusalWithNoAlternateIsHonest(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{}
	f.install(t)

	prior := []attempt{{provider: "enso", status: 402, fault: faultProvider,
		err: errors.New("Insufficient credits.")}}

	var w buffer
	a := newAsk(route("enso"), &w, nil) // family passthrough: primary only, no fallbacks
	a.prior = prior
	_, _, _, err := a.serve()
	if err == nil {
		t.Fatal("nobody could serve this; want an error")
	}
	if len(f.asked) != 0 {
		t.Errorf("asked %v — the only vendor already refused", f.asked)
	}
	if !strings.Contains(err.Error(), "Insufficient credits") || !strings.Contains(err.Error(), "enso") {
		t.Errorf("error %q must name the vendor and quote its reason", err.Error())
	}
	if got := statusOf(err); got == 402 {
		t.Error("the upstream's 402 must not reach the customer as a 402")
	}
}

// ── the streaming decision ──────────────────────────────────────────────────

// Once a byte has reached the client the request is committed. Moving it would
// emit a second answer after the first one already started.
func TestNoFailoverOnceTheStreamHasOpened(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	var w buffer
	f := &fleet{
		answer: map[string]error{"do-ai": apiErr(500, "died mid-answer")},
		wrote:  map[string]string{"do-ai": "The answer is"},
	}
	f.install(t)

	// The writer reports that bytes are on the wire as soon as it holds any —
	// exactly what OpenAIWriter.StreamSent means.
	_, _, _, err := newAsk(route("do-ai", "anthropic"), &w, func() bool { return w.text != "" }).serve()
	if err == nil {
		t.Fatal("want the failure surfaced, not a second answer stitched on")
	}
	if len(f.asked) != 1 {
		t.Errorf("asked %v — failing over after the first token duplicates output, "+
			"which is worse than not failing over at all", f.asked)
	}
	if strings.Contains(strings.Join(f.asked, ","), "anthropic") {
		t.Error("the alternate must NOT be asked once the client is already reading")
	}
}

// The same rule applies to asking the SAME vendor again, which is the case the
// cross-provider guard above cannot see: a 429 or 503 is transient, so the retry
// loop would normally ask once more — but not after tokens have gone out. This
// is the guard inside the retry, and it is the one that stops a client reading
// the first half of an answer twice.
func TestNoSameProviderRetryOnceTheStreamHasOpened(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)
	// Two tries per vendor: without the guard the transient 503 below earns a
	// second call, and the second call writes the same tokens again.
	defaultRetryPolicy = retryPolicy{attempts: 2, base: time.Millisecond, max: time.Millisecond}

	var w buffer
	f := &fleet{
		answer: map[string]error{"do-ai": apiErr(503, "died mid-answer")},
		wrote:  map[string]string{"do-ai": "Hello"},
	}
	f.install(t)

	_, _, _, err := newAsk(route("do-ai"), &w, func() bool { return w.text != "" }).serve()
	if err == nil {
		t.Fatal("want the failure surfaced")
	}
	if len(f.asked) != 1 {
		t.Errorf("do-ai was asked %d times — retrying a vendor that already emitted tokens "+
			"sends the client the same partial answer twice", len(f.asked))
	}
	if w.text != "Hello" {
		t.Errorf("client would receive %q, want %q — a replayed attempt duplicated output", w.text, "Hello")
	}
}

// The other half of the streaming rule: while nothing has been flushed, the
// buffered bytes of a dead attempt must be discarded, or the next provider's
// answer is served glued to the dead one's half-sentence.
func TestFailedAttemptLeavesNothingInTheWriter(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{
		answer: map[string]error{"do-ai": apiErr(503, "died after three tokens")},
		wrote:  map[string]string{"do-ai": "The capital of France is Berl", "anthropic": "The capital of France is Paris."},
	}
	f.install(t)

	var w buffer
	// sent stays false: nothing was flushed to the client, so the request is
	// still movable — this is the non-streaming (buffered) case.
	_, by, _, err := newAsk(route("do-ai", "anthropic"), &w, func() bool { return w.sent }).serve()
	if err != nil {
		t.Fatalf("the alternate must serve: %v", err)
	}
	if by.name != "anthropic" {
		t.Fatalf("served by %q, want anthropic", by.name)
	}
	if w.text != "The capital of France is Paris." {
		t.Errorf("client would receive %q — a failed attempt's partial text must never be "+
			"prepended to the answer that actually served", w.text)
	}
}

// A client that hung up must not be answered at somebody else's expense.
func TestNoFailoverAfterTheCallerHangsUp(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{"do-ai": apiErr(503, "down")}}
	f.install(t)

	gone, hangUp := context.WithCancel(context.Background())
	var w buffer
	a := newAsk(route("do-ai", "anthropic"), &w, nil)
	a.ctx = gone
	hangUp()

	_, _, _, err := a.serve()
	if err == nil {
		t.Fatal("want the cancellation surfaced")
	}
	if len(f.asked) != 0 {
		t.Errorf("asked %v — nobody is listening; every one of those calls is money spent "+
			"on an answer that goes nowhere", f.asked)
	}
}

// The row that ANSWERED travels back, because that is the row whose credential
// was spent and the one the BYO fee decision must read.
func TestServedCarriesTheRowThatSpentTheCredential(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{"do-ai": apiErr(429, "busy")}}
	f.install(t)

	var w buffer
	_, by, _, err := newAsk(route("do-ai", "anthropic"), &w, nil).serve()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if by.row == nil {
		t.Fatal("no row came back — billing would attribute the call to whatever auth resolved first")
	}
	if by.row.Name != "anthropic" {
		t.Errorf("row = %q, want anthropic — the fallback served, so the fallback's row is the one that paid", by.row.Name)
	}
}
