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
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// A RACE COSTS N COMPLETIONS AND SETTLES ONE, and the gap between those two
// numbers is real money. These tests are about that gap: who is billed, from
// which writer, and what the caller is handed.
//
// The mechanism ships inert — nothing sets ask.fan, so every request in
// production today takes the cascade. That is exactly why the coverage has to be
// here rather than deferred to the switch being wired: the day someone sets a
// number, this is what says the money was already right.

// racers scripts providers that take TIME, which is the only way to decide a
// race. Each one emits its text a chunk at a time so a loser can be caught
// mid-sentence, which is the state the ledger has to price.
type racers struct {
	// after is how long each provider waits before its first chunk. The smallest
	// wins.
	after map[string]time.Duration
	// says is what each provider emits, in chunks, once it starts.
	says map[string][]string
	// fail names providers that refuse instead of answering.
	fail map[string]error

	mu   sync.Mutex
	got  map[string]string // what each provider actually managed to write
	seen []string
}

func (r *racers) install(t *testing.T) {
	t.Helper()
	r.got = map[string]string{}
	saved := callProvider
	t.Cleanup(func() { callProvider = saved })
	callProvider = func(org string, row *object.Provider, c candidate, question string,
		w io.Writer, history, knowledge []*model.RawMessage, lang string,
	) (*model.ModelResult, *object.Provider, error) {
		r.mu.Lock()
		r.seen = append(r.seen, c.provider)
		r.mu.Unlock()

		if err, ok := r.fail[c.provider]; ok && err != nil {
			return nil, &object.Provider{Owner: "admin", Name: c.provider}, err
		}
		time.Sleep(r.after[c.provider])

		wrote := ""
		for _, chunk := range r.says[c.provider] {
			if _, err := w.Write([]byte(chunk)); err != nil {
				// hedge.ErrLost. A provider that reads its write error stops
				// here — mid-answer, having already been billed for what it
				// produced. This is the loser the ledger must price.
				break
			}
			wrote += chunk
			time.Sleep(2 * time.Millisecond)
		}
		r.mu.Lock()
		r.got[c.provider] = wrote
		r.mu.Unlock()

		return &model.ModelResult{ResponseTokenCount: 7}, &object.Provider{Owner: "admin", Name: c.provider}, nil
	}
}

// body is the response the client is reading — the destination a race writes
// its winner into, and nothing else.
type body struct {
	mu   sync.Mutex
	text string
}

func (s *body) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text += string(p)
	return len(p), nil
}

func (s *body) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text
}

// plain is a stand-in for OpenAIWriter: it appends, it reports what it holds,
// and a fork of it starts empty. That is the whole contract race() depends on.
type plain struct {
	out io.Writer
	buf bytes.Buffer
}

func (p *plain) Write(b []byte) (int, error) {
	n, err := p.out.Write(b)
	if n > 0 {
		p.buf.Write(b[:n])
	}
	return n, err
}
func (p *plain) MessageString() string { return p.buf.String() }

func fanOf(n int, to io.Writer, adopted **plain, billed chan<- attempt) *fan {
	return &fan{
		n:    n,
		to:   to,
		fork: func(out io.Writer) io.Writer { return &plain{out: out} },
		adopt: func(won io.Writer) {
			if w, ok := won.(*plain); ok {
				*adopted = w
			}
		},
		bill: func(at attempt) {
			if billed != nil {
				billed <- at
			}
		},
	}
}

// waitBill takes the next ledger row, or says the bill never came. A loser is
// settled from its own goroutine LONG after the answer has gone out, so a test
// that reads without waiting is testing the scheduler.
func waitBill(t *testing.T, billed <-chan attempt) attempt {
	t.Helper()
	select {
	case at := <-billed:
		return at
	case <-time.After(5 * time.Second):
		t.Fatal("no ledger row for the beaten provider — it burned real tokens upstream " +
			"and nothing recorded them, which is fast mode costing money invisibly")
		return attempt{}
	}
}

// THE BILLING RULE. A raced loser was cancelled mid-generation, so the vendor
// produced tokens and charged us. It must arrive at the ledger carrying a real
// count — never as the free refusal a failover attempt is.
func TestARacedLoserIsBilledForWhatItProduced(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	r := &racers{
		after: map[string]time.Duration{"enso": 0, "do-ai": 40 * time.Millisecond},
		says: map[string][]string{
			"enso":  {"the winner answers"},
			"do-ai": {"the loser ", "got this far ", "before it was cut"},
		},
	}
	r.install(t)

	var out body
	var won *plain
	billed := make(chan attempt, 4)
	a := newAsk(route("enso", "do-ai"), nil, nil)
	a.prompt = 11
	a.fan = fanOf(2, &out, &won, billed)

	res, by, tried, err := a.serve()
	if err != nil {
		t.Fatalf("a race one provider won must not fail: %v", err)
	}
	if res == nil || by.name != "enso" {
		t.Fatalf("served by %q, want enso — the first to produce a byte wins", by.name)
	}
	// The beaten provider is deliberately NOT here. What the caller is handed it
	// records as refusals that cost nothing, and that is the one row this must
	// never become.
	for _, at := range tried {
		if at.provider == "do-ai" {
			t.Fatal("the beaten provider came back in the attempts, where the caller " +
				"files it as a free refusal — the exact row that hides what fast mode spent")
		}
	}

	loser := waitBill(t, billed)
	if loser.provider != "do-ai" {
		t.Fatalf("billed %q, want the provider that lost", loser.provider)
	}
	if loser.prompt != 11 {
		t.Errorf("loser prompt=%d, want 11: the vendor accepted and processed the prompt "+
			"and charged us for it, whether or not it went on to win", loser.prompt)
	}
	if loser.completion <= 0 {
		t.Errorf("loser completion=%d, want >0: it emitted %q before it was cancelled, and "+
			"a zero here files a real upstream charge as a free refusal",
			loser.completion, r.got["do-ai"])
	}
}

// The counts must come from the LOSER's own writer. One writer shared across
// attempts holds exactly one answer, and it is the winner's — so a shared writer
// bills every loser for the winner's tokens, which is both wrong and invisible.
func TestEachRacerIsBilledFromItsOwnWriter(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	// The loser says far more than the winner. If the counts were read from one
	// shared writer, the loser's would match the winner's instead.
	r := &racers{
		after: map[string]time.Duration{"enso": 0, "do-ai": 30 * time.Millisecond},
		says: map[string][]string{
			"enso":  {"short"},
			"do-ai": {strings.Repeat("a much longer answer that keeps going ", 12)},
		},
	}
	r.install(t)

	var out body
	var won *plain
	billed := make(chan attempt, 4)
	a := newAsk(route("enso", "do-ai"), nil, nil)
	a.prompt = 5
	a.fan = fanOf(2, &out, &won, billed)

	if _, _, _, err := a.serve(); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if won == nil {
		t.Fatal("no writer was adopted, so the caller has no answer to return")
	}
	loser := waitBill(t, billed)
	winner, _ := model.GetTokenSize("test-model", won.MessageString())
	if loser.completion == winner {
		t.Errorf("the loser was billed %d tokens, the same as the winner — the counts "+
			"are being read from one shared writer rather than each attempt's own",
			loser.completion)
	}
	if loser.completion <= winner {
		t.Errorf("the loser said far more than the winner and was billed %d against %d",
			loser.completion, winner)
	}
	if got := out.String(); got != "short" {
		t.Errorf("the caller received %q, want only the winner's bytes: a loser's "+
			"half-sentence glued to the winner's answer reads as a model losing its mind", got)
	}
}

// The winner's writer becomes the caller's. Without this a non-streaming reply
// is assembled from the writer the handler has held all along — which, in a
// race, is the one writer nobody wrote to.
func TestTheCallerReadsTheWinnersAnswer(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	r := &racers{
		after: map[string]time.Duration{"enso": 0, "do-ai": 50 * time.Millisecond},
		says:  map[string][]string{"enso": {"hello ", "from enso"}, "do-ai": {"never seen"}},
	}
	r.install(t)

	var out body
	var won *plain
	a := newAsk(route("enso", "do-ai"), nil, nil)
	a.fan = fanOf(2, &out, &won, nil)

	if _, _, _, err := a.serve(); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if won == nil {
		t.Fatal("nothing was adopted: a non-streaming reply built from the caller's own " +
			"writer would be empty, and would be returned as a successful answer")
	}
	if got := won.MessageString(); got != "hello from enso" {
		t.Errorf("adopted answer %q, want the winner's whole message", got)
	}
}

// A REFUSAL STILL BILLS NOTHING. The rule the cascade has always had must
// survive the one being added beside it, or every failover in production starts
// charging customers for vendors that refused them.
func TestARefusalCarriesNoTokens(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{"enso": apiErr(402, "Insufficient credits.")}}
	f.install(t)

	var w buffer
	_, _, tried, err := newAsk(route("enso", "do-ai"), &w, nil).serve()
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	for _, at := range tried {
		if at.prompt != 0 || at.completion != 0 {
			t.Errorf("provider %s refused and carries prompt=%d completion=%d, want zero both: "+
				"an attempt that produced nothing must reach the ledger free",
				at.provider, at.prompt, at.completion)
		}
	}
}

// A fan too narrow to race takes the cascade, so a request that asks for fast
// mode on a model with one vendor still gets the retry and the demotion rather
// than a bare single call.
func TestAFanOfOneTakesTheCascade(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{"enso": apiErr(402, "Insufficient credits.")}}
	f.install(t)

	var out body
	var won *plain
	var w buffer
	a := newAsk(route("enso", "do-ai"), &w, nil)
	a.fan = fanOf(1, &out, &won, nil)

	_, by, _, err := a.serve()
	if err != nil {
		t.Fatalf("a fan of one must behave exactly like no fan at all: %v", err)
	}
	if by.name != "do-ai" {
		t.Errorf("served by %q, want do-ai — the cascade's failover must still happen", by.name)
	}
	if strings.Join(f.asked, ",") != "enso,do-ai" {
		t.Errorf("asked %v, want the providers in turn", f.asked)
	}
}

// THE MECHANISM IS INERT UNTIL SOMEBODY SETS A NUMBER. Nothing in production
// sets ask.fan today, and this is what says so: with it nil, the request takes
// the same path, in the same order, as it did before any of this existed.
func TestWithoutAFanNothingChanges(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	f := &fleet{answer: map[string]error{
		"enso":  apiErr(500, "boom"),
		"do-ai": apiErr(500, "boom"),
	}}
	f.install(t)

	var w buffer
	_, _, tried, err := newAsk(route("enso", "do-ai", "openai"), &w, nil).serve()
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if strings.Join(f.asked, ",") != "enso,do-ai,openai" {
		t.Errorf("asked %v, want them one at a time in declared order", f.asked)
	}
	if len(tried) != 2 {
		t.Fatalf("tried %d, want the 2 that refused", len(tried))
	}
	for _, at := range tried {
		if at.prompt != 0 || at.completion != 0 {
			t.Errorf("%s carries tokens on the unhedged path", at.provider)
		}
	}
}

// Every racer refusing is not a silent empty answer. The caller must be told who
// was asked, the same way the cascade names them.
func TestEveryRacerRefusingNamesThemAll(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)

	r := &racers{fail: map[string]error{
		"enso":  apiErr(500, "boom"),
		"do-ai": apiErr(503, "down"),
	}}
	r.install(t)

	var out body
	var won *plain
	a := newAsk(route("enso", "do-ai"), nil, nil)
	a.fan = fanOf(2, &out, &won, nil)

	res, _, _, err := a.serve()
	if err == nil {
		t.Fatal("every provider refused and the race reported success")
	}
	if res != nil {
		t.Error("a result came back from a race nobody won")
	}
	// Named, not summarised. "the model is unavailable" sends whoever reads it to
	// the wrong place; the cascade says who was asked and so must this.
	for _, name := range []string{"enso", "do-ai"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error does not name %s: %v", name, err)
		}
	}
}
