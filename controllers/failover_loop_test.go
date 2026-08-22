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
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

// A DEAD CREDENTIAL FAILS OVER, AND IS STILL LOUD. Those are two jobs, and this
// used to do the second by refusing to do the first.
//
// The refusal is a fact about the VENDOR — our key will not open it — and the
// caller's own credential was checked at our edge long before any vendor was
// dialled, so nothing about their request is wrong. announce() counts
// cloud_supply_refused{reason="credential"} and logs at ERROR whichever way the
// request goes, so surfacing costs the failover nothing.
//
// Measured, the old coupling cost exactly what it was meant to prevent:
// DO_AI_API_KEY died and 93 routes answered 401 to CUSTOMERS for days — louder
// than any alert and aimed at the wrong people — while the metric that was meant
// to raise a hand had been sitting there the whole time.
func TestUnauthorizedFailsOverAndIsStillLoud(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{"do-ai": apiErr(401, "invalid api key")}}
	f.install(t)

	var w buffer
	_, served, tried, err := newAsk(route("do-ai", "anthropic"), &w, nil).serve()
	if err != nil {
		t.Fatalf("a dead key of ours must not become the customer's error: %v", err)
	}
	if served.name != "anthropic" {
		t.Errorf("served by %q, want anthropic — the request must reach a vendor that can answer", served.name)
	}
	if len(f.asked) != 2 {
		t.Errorf("asked %v — the dead vendor is tried once, then the request moves", f.asked)
	}
	if len(tried) != 1 || tried[0].status != 401 {
		t.Errorf("the 401 must be RECORDED as an attempt, not swallowed: %+v", tried)
	}
	// Rested like an empty account: both need a human, and neither clears itself
	// in seconds, so the fleet must not return to it every few seconds.
	if !cooled.cooling(credential{"", "do-ai"}) {
		t.Error("a vendor whose key is dead must be demoted, or every request pays its 401 first")
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
	if !cooled.cooling(credential{"", "enso"}) {
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

// The loop must file its demotion against the account that earned it.
//
// This is the WRITE side of the property TestOneTenantsEmptyAccountIsNotAnothersOutage
// asserts on the READ side. Both are needed: with only one, dropping the org at
// the other end restores the cross-tenant leak with every other test still green.
func TestTheLoopDemotesTheAccountNotTheVendor(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{"enso": apiErr(402, "Insufficient credits.")}}
	f.install(t)

	var w buffer
	a := newAsk(route("enso", "do-ai"), &w, nil)
	a.org = "org-a"
	if _, _, _, err := a.serve(); err != nil {
		t.Fatalf("serve: %v", err)
	}

	if !cooled.cooling(credential{"org-a", "enso"}) {
		t.Error("the org whose key answered 402 must be resting — otherwise its next request " +
			"pays the empty account's round trip all over again")
	}
	if cooled.cooling(credential{"org-b", "enso"}) {
		t.Error("another tenant inherited the penalty; its request spends a different credential")
	}
	if cooled.cooling(credential{"", "enso"}) {
		t.Error("the refusal was filed against the VENDOR rather than the account that earned it, " +
			"which is the whole cross-tenant leak")
	}
}

// Both sites that learn from a refusal learn the same thing, so both are asked
// the same question here. A refusal rests the ACCOUNT that answered it; no other
// tenant and no other vendor inherits anything.
func TestARefusalRestsOneAccount(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)

	if d := cooled.rest("org-a", "enso", apiErr(402, "Insufficient credits.")); d != coolBroke {
		t.Fatalf("an empty account rests %v, want %v", d, coolBroke)
	}
	if !cooled.cooling(credential{"org-a", "enso"}) {
		t.Error("the account that refused must be resting")
	}
	if cooled.cooling(credential{"org-b", "enso"}) {
		t.Error("another tenant inherited it")
	}
	if cooled.cooling(credential{"org-a", "do-ai"}) {
		t.Error("another vendor inherited it")
	}

	// A DEAD KEY RESTS AS LONG AS AN EMPTY ACCOUNT. It is the same operational
	// class — one needs a human with a credit card, the other a human with a new
	// key — and neither clears itself in seconds, so a short rest would put every
	// request back on a vendor that cannot answer.
	if d := cooled.rest("org-a", "do-ai", apiErr(401, "invalid api key")); d != coolBroke {
		t.Errorf("a 401 rested the vendor for %v, want %v", d, coolBroke)
	}
	if !cooled.cooling(credential{"org-a", "do-ai"}) {
		t.Error("the vendor whose key is dead must be resting")
	}
	// And it is still ONE account's problem: another tenant's credential for the
	// same vendor is untouched.
	if cooled.cooling(credential{"org-b", "do-ai"}) {
		t.Error("another tenant inherited our dead key")
	}
}

// breaks is an http.ResponseWriter that delivers `ok` writes and then fails —
// the shape of an upstream dying mid-answer, after the client already has bytes.
type breaks struct {
	*httptest.ResponseRecorder
	ok int
}

func breaksAfter(ok int) *breaks { return &breaks{httptest.NewRecorder(), ok} }

// tears delivers PART of a write and then reports the failure — a connection that
// broke mid-event. n > 0 with a non-nil error is the case the flag has to get
// right: those bytes are on the wire whatever the error says about the rest.
type tears struct{ *httptest.ResponseRecorder }

func (t *tears) Write(p []byte) (int, error) {
	n, _ := t.ResponseRecorder.Write(p[:len(p)/2])
	return n, errors.New("connection reset by peer")
}

func (b *breaks) Write(p []byte) (int, error) {
	if b.ok <= 0 {
		return 0, errors.New("connection reset by peer")
	}
	b.ok--
	return b.ResponseRecorder.Write(p)
}

// A8. StreamSent is the whole movability decision, and it was set at the END of
// Write — after message_start and content_block_start had already gone out.
//
// So there was a window, 185 measured bytes wide, in which the client held the
// opening of one vendor's answer and the loop still believed the request could be
// offered to another. It would then have received the whole of a second vendor's
// answer glued to the first one's first breath.
func TestTheAnthropicWriterSaysSentAsSoonAsBytesGoOut(t *testing.T) {
	// Deliver message_start and content_block_start, then fail — exactly the gap.
	rec := breaksAfter(2)
	w := &AnthropicWriter{
		Writer:    bufio.NewWriter(rec),
		RequestID: "req-1",
		Stream:    true,
		Cleaner:   *NewCleaner(6),
		Model:     "claude-x",
	}

	_, err := w.Write([]byte("2 + 2 = 4"))
	if err == nil {
		t.Fatal("the scripted writer must fail on the delta")
	}
	if rec.Body.Len() == 0 {
		t.Fatal("nothing was written; this test proves nothing")
	}
	if !w.StreamSent {
		t.Errorf("%d bytes reached the client and StreamSent is false — the loop would move "+
			"this request and the customer would read two vendors' answers spliced together",
			rec.Body.Len())
	}
	if !strings.Contains(rec.Body.String(), "message_start") {
		t.Errorf("expected the header events on the wire, got:\n%s", rec.Body.String())
	}
}

// A half-written event is still a written event. The bytes are gone; whether the
// call that sent them also returned an error changes nothing about that.
func TestAPartialWriteCountsAsSent(t *testing.T) {
	rec := &tears{httptest.NewRecorder()}
	w := &AnthropicWriter{
		Writer:    bufio.NewWriter(rec),
		RequestID: "req-1",
		Stream:    true,
		Cleaner:   *NewCleaner(6),
		Model:     "claude-x",
	}
	if _, err := w.Write([]byte("2 + 2 = 4")); err == nil {
		t.Fatal("the scripted writer must report the break")
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the scripted writer delivered nothing; this test proves nothing")
	}
	if !w.StreamSent {
		t.Errorf("%d bytes are on the wire after a failed write and StreamSent is false — "+
			"reading the error instead of the byte count is how a half-sent answer "+
			"gets a second vendor's answer appended to it", rec.Body.Len())
	}
}

// The other half: a writer that has sent nothing is still movable, or a single
// transient failure on the first byte would strand every request it touched.
func TestTheAnthropicWriterIsMovableUntilItWrites(t *testing.T) {
	rec := breaksAfter(0)
	w := &AnthropicWriter{
		Writer:    bufio.NewWriter(rec),
		RequestID: "req-1",
		Stream:    true,
		Cleaner:   *NewCleaner(6),
		Model:     "claude-x",
	}
	if _, err := w.Write([]byte("2 + 2 = 4")); err == nil {
		t.Fatal("the scripted writer must fail on the first event")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("the scripted writer delivered %d bytes; it must deliver none", rec.Body.Len())
	}
	if w.StreamSent {
		t.Error("nothing reached the client, so this request is still movable — saying " +
			"otherwise turns one transient failure into a refusal we could have routed around")
	}
}

// pipeFamily drives the real family pipe against a family answering `status`,
// with a funded reservation, and reports what came back and what the ledger says.
func pipeFamily(t *testing.T, subject string, status int, body string) (refused []attempt, avail int64) {
	t.Helper()
	family := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer family.Close()

	fam := &modelFamily{
		name: "enso", prefix: "enso",
		providerFn: func() *object.Provider {
			return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: family.URL}
		},
	}

	object.GlobalBalanceLedger.SetBalance(subject, 1000)
	hold, ok := reserveBudget(subject, 100)
	if !ok {
		t.Fatal("reserve against a funded subject must succeed")
	}
	if a, _ := object.GlobalBalanceLedger.Available(subject); a != 900 {
		t.Fatalf("available after reserve = %d, want 900", a)
	}

	req := []byte(`{"model":"enso-flash","messages":[{"role":"user","content":"hi"}]}`)
	c := visit("POST", "/v1/chat/completions")

	refused = c.pipeToFamily(fam, "chat/completions", "openai", "enso-flash", req, false,
		"org-a", nil, false, hold, time.Now())
	avail, _ = object.GlobalBalanceLedger.Available(subject)
	return refused, avail
}

// A1. What the pipe returns and what it owes the reservation are the same fact,
// so they are asserted together.
//
// nil means the request is OVER: nothing downstream will run, so the cents must
// be back. A refusal means the request is still MOVING: the cents must still be
// held, because whichever provider ends up serving it has to be billed and
// settle only fires once.
//
// The leak this pins: a 400 from an embeddings family returned nil, and nothing
// anywhere released the hold. The customer's balance simply stayed short.
func TestTheHoldFollowsTheAnswer(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)

	t.Run("a refusal nobody else can fix ends the request and gives the cents back", func(t *testing.T) {
		refused, avail := pipeFamily(t, "holdtest/req-fault", http.StatusBadRequest,
			`{"error":{"message":"invalid 'messages': empty"}}`)
		if refused != nil {
			t.Fatalf("a 400 is the request's fault; no other vendor changes it, got %+v", refused)
		}
		if avail != 1000 {
			t.Errorf("available = %d, want 1000 — the request is over and nothing downstream "+
				"will ever settle this hold, so the customer is short by the reservation", avail)
		}
	})

	t.Run("a refusal somebody else can fix keeps the cents held", func(t *testing.T) {
		refused, avail := pipeFamily(t, "holdtest/provider-fault", http.StatusPaymentRequired,
			`{"error":{"message":"Insufficient credits."}}`)
		if len(refused) != 1 {
			t.Fatalf("a 402 is the vendor's fault and the request must stay movable, got %+v", refused)
		}
		if avail != 900 {
			t.Errorf("available = %d, want 900 — releasing here is one-shot, and whichever "+
				"provider serves this next would then be billed nothing", avail)
		}
	})

	// A caller who hangs up is also an ending — there is nobody left to serve, so
	// the request is not offered anywhere else. That makes it a nil, and a nil
	// owes the cents back. It is the one ending that reaches nil through the
	// refusal path rather than around it, which is exactly how it was missed.
	t.Run("a caller who hung up ends the request and gives the cents back", func(t *testing.T) {
		const subject = "holdtest/hangup"
		fam := &modelFamily{
			name: "enso", prefix: "enso",
			providerFn: func() *object.Provider {
				// Nothing listens here: the dial fails and the pipe reaches the
				// refusal path, where it finds the caller already gone.
				return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: "http://127.0.0.1:1"}
			},
		}
		object.GlobalBalanceLedger.SetBalance(subject, 1000)
		hold, ok := reserveBudget(subject, 100)
		if !ok {
			t.Fatal("reserve must succeed")
		}

		gone, hangUp := context.WithCancel(context.Background())
		hangUp()
		req := []byte(`{"model":"enso-flash","messages":[{"role":"user","content":"hi"}]}`)
		c := visit(http.MethodPost, "/v1/chat/completions")
		// Nobody is listening, and the handler learns that the way it does in
		// production: off the request it was handed.
		c.SetContext(gone)

		if refused := c.pipeToFamily(fam, "chat/completions", "openai", "enso-flash", req, false,
			"org-a", nil, false, hold, time.Now()); refused != nil {
			t.Fatalf("nobody is listening; the request must not be offered elsewhere, got %+v", refused)
		}
		if avail, _ := object.GlobalBalanceLedger.Available(subject); avail != 1000 {
			t.Errorf("available = %d, want 1000 — the request is over and nothing will settle "+
				"this hold, so a client that disconnects leaves its own balance short", avail)
		}
	})

	t.Run("a served request settles its real cost", func(t *testing.T) {
		refused, avail := pipeFamily(t, "holdtest/served", http.StatusOK,
			`{"id":"x","object":"chat.completion","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		if refused != nil {
			t.Fatalf("a 200 is served, got %+v", refused)
		}
		if avail < 900 || avail > 1000 {
			t.Errorf("available = %d, want the hold released and the real cost debited", avail)
		}
	})
}

// The family pipe is the OTHER site that learns from a refusal, and it is the
// one that served the outage. Driven end to end against a family answering the
// measured 402, so the org has to survive the whole call rather than only the
// helper it eventually reaches.
func TestTheFamilyPipeRestsTheCallersAccount(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)

	family := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"Insufficient credits."}}`))
	}))
	defer family.Close()

	fam := &modelFamily{
		name: "enso", prefix: "enso",
		providerFn: func() *object.Provider {
			return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: family.URL}
		},
	}

	body := []byte(`{"model":"enso-flash","messages":[{"role":"user","content":"hi"}]}`)
	c := visit("POST", "/v1/chat/completions")

	refused := c.pipeToFamily(fam, "chat/completions", "openai", "enso-flash", body, false,
		"org-a", nil, false, nil, time.Now())

	if len(refused) != 1 || refused[0].status != 402 {
		t.Fatalf("the pipe must hand back the refusal so the request can move, got %+v", refused)
	}
	if !cooled.cooling(credential{"org-a", "enso"}) {
		t.Error("the calling org's account must be resting after its 402")
	}
	if cooled.cooling(credential{"org-b", "enso"}) {
		t.Error("a different tenant inherited the family's 402 — it spends a different credential")
	}
	if cooled.cooling(credential{"", "enso"}) {
		t.Error("the 402 was filed against the family rather than against the account that earned it")
	}
}

// Demotion is a preference, never a veto: a model whose only vendor is resting
// must still be attempted rather than refused.
func TestCoolingProviderIsStillTriedWhenItIsTheOnlyOne(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	cooled.demote(credential{"", "do-ai"}, time.Minute)
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
	_, _, tried, err := newAsk(route("do-ai", "anthropic"), &w, func() bool { return w.text != "" }).serve()
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

	// The two assertions above are not enough on their own, and this test used to
	// stop there. Removing the guard in serve() leaves them BOTH green, because the
	// guard inside call() still refuses to dial a second vendor — but the loop has
	// already entered that iteration, so it records an attempt against a vendor
	// nobody contacted, and recordRefusals then writes a ledger row blaming it. It
	// also replaces the real reason with our own "cannot retry".
	if len(tried) != 1 || tried[0].provider != "do-ai" {
		t.Errorf("tried = %+v, want exactly the one vendor that actually answered — "+
			"a refusal recorded against a vendor we never dialled is a ledger row "+
			"accusing an innocent party", tried)
	}
	if !strings.Contains(err.Error(), "died mid-answer") {
		t.Errorf("err = %v, want the upstream's real reason. Once it reads as our own "+
			"\"response partially written\", whoever debugs this is looking at us "+
			"instead of at the vendor that died", err)
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
