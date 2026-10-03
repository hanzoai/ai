// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"sync/atomic"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// A plan covers Hanzo SKUs: Enso's and Zen's, and the pool's own name, which Enso
// answers. Every other model is a third-party one.
func TestFamilyOfNamesHanzoSKUsOnly(t *testing.T) {
	for model, want := range map[string]string{
		"enso": "enso", "enso-pro": "enso", "Enso-Flash": "enso", "hanzo/enso": "enso", "free": "enso",
		"zen5": "zen", "zen6-flash": "zen",
		"anthropic/claude-opus-5.5": "", "openai/gpt-6-sol": "", "vendor/small:free": "", "": "",
	} {
		if got := FamilyOf(model); got != want {
			t.Errorf("FamilyOf(%q) = %q, want %q", model, got, want)
		}
	}
}

// A family's stated cost reads in nano-dollars, rounded up; a missing, malformed or
// negative figure is no figure at all.
func TestAStatedCostReadsInNanos(t *testing.T) {
	for in, want := range map[string]int64{"0.0025": 2_500_000, "0.0000000001": 1, "0": 0, "12.5": 12_500_000_000} {
		if got, ok := usdNanos(in); !ok || got != want {
			t.Errorf("usdNanos(%q) = %d, %t; want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "lots", "-1", "1,2"} {
		if _, ok := usdNanos(bad); ok {
			t.Errorf("%q read as a cost", bad)
		}
	}
	if got := spendUSD(50_000_000); got != "0.050000000" {
		t.Errorf("spendUSD = %q", got)
	}
}

// committed answers the way a family answers a request its plan opened a paid rung
// for: 200 at once with the most the request can cost (bound, none when empty) and
// its cost declared as a trailer, then body, then the cost (none when cost is empty).
func committed(w http.ResponseWriter, sse bool, bound, body, cost string) {
	if sse {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	if bound != "" {
		w.Header().Set(costBoundHeader, bound)
	}
	w.Header().Set("Trailer", costHeader)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
	if cost != "" {
		w.Header().Set(costHeader, cost)
	}
}

// cutAfter writes a committed answer's head and body chunks by hand and drops the
// connection before the body ends: no trailer ever arrives.
func cutAfter(w http.ResponseWriter, sse bool, bound string, chunks ...string) {
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	ct := "application/json"
	if sse {
		ct = "text/event-stream"
	}
	head := "HTTP/1.1 200 OK\r\nContent-Type: " + ct + "\r\nTrailer: " + costHeader + "\r\nTransfer-Encoding: chunked\r\n"
	if bound != "" {
		head += costBoundHeader + ": " + bound + "\r\n"
	}
	_, _ = rw.WriteString(head + "\r\n")
	for _, c := range chunks {
		_, _ = fmt.Fprintf(rw, "%x\r\n%s\r\n", len(c), c)
	}
	_ = rw.Flush()
}

// paidSSE is a paid rung's whole answer on a committed stream: the paid marker, the
// words, the usage and the end.
const paidSSE = ": keep-alive\n\n: paid\n\n" +
	`data: {"id":"x","model":"enso-pro","choices":[{"delta":{"content":"ok"}}]}` + "\n\n" +
	`data: {"id":"x","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":2000}}` + "\n\ndata: [DONE]\n\n"

// freeSSE is a free rung's whole answer on a committed stream: no paid marker.
const freeSSE = ": keep-alive\n\n" +
	`data: {"id":"x","model":"enso-pro","choices":[{"delta":{"content":"ok"}}]}` + "\n\n" +
	`data: {"id":"x","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":2000}}` + "\n\ndata: [DONE]\n\n"

// planEnso stands up an Enso service that records the spend each request carried and
// answers it committed, from a paid rung when paid is set, at cost.
func planEnso(t *testing.T, paid bool, cost string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var spends []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		spends = append(spends, r.Header.Get(spendHeader))
		mu.Unlock()
		body := freeSSE
		if paid {
			body = paidSSE
		}
		committed(w, true, "0.004", body, cost)
	}))
	t.Cleanup(srv.Close)
	restore(t, ensoFam)
	ensoFam.providerFn = func() *object.Provider {
		return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: srv.URL}
	}
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro", Plan: true}}
	ensoFam.ids = []string{"enso-pro"}
	ensoFam.loaded, ensoFam.fetchedAt = true, time.Now()
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), spends...) }
}

// A request its plan covers carries what the plan holds to Enso, and settles against
// the plan at what Enso states the answer cost: the usage record says the plan covered
// it, so no wallet is debited and no free allowance counted. A free rung's answer
// costs nothing.
func TestAPlanCoveredFamilyCallCarriesSpendAndSettlesItsCost(t *testing.T) {
	for _, tc := range []struct {
		name string
		paid bool
		cost string
		want int64
	}{
		{"a paid rung answered", true, "0.0025", 2_500_000},
		{"a free rung answered", false, "0", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cooled.forget()
			_, spends := planEnso(t, tc.paid, tc.cost)

			var mu sync.Mutex
			var events []object.UsageEvent
			prev := object.UsageRecorder()
			object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
				mu.Lock()
				events = append(events, u)
				mu.Unlock()
				return nil
			})
			t.Cleanup(func() { object.SetUsageRecorder(prev) })

			var settled []int64
			grant := &object.LimitGrant{Plan: "max-20x", Spend: 50_000_000, Settle: func(n int64) {
				mu.Lock()
				settled = append(settled, n)
				mu.Unlock()
			}}
			body := []byte(`{"model":"enso-pro","messages":[{"role":"user","content":"hi"}]}`)
			c := visit(http.MethodPost, "/v1/chat/completions")
			c.Fiber().Request().SetBody(body)
			Cover(c.Ctx, grant)
			user := &iam.User{Owner: "acme", Name: "ann"}
			if out := c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-pro", body, false, 0, "acme", user, false, nil, time.Now()); out != nil {
				t.Fatalf("attempts=%+v, want Enso to answer", out)
			}
			drain(t, c)

			if got := spends(); len(got) != 1 || got[0] != "0.050000000" {
				t.Fatalf("Enso was sent spend %v, want the plan's hold in USD", got)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(settled) == 0 || settled[0] != tc.want {
				t.Fatalf("settled %v, want %d first", settled, tc.want)
			}
			for _, n := range settled[1:] {
				if n != 0 {
					t.Fatalf("settled %v: a later settle charged more", settled)
				}
			}
			if len(events) != 1 || !events[0].Plan {
				t.Fatalf("usage events %+v, want one marked as the plan's", events)
			}
		})
	}
}

// Without a grant, or with one holding nothing, no spend travels: the family answers
// from its free rungs.
func TestNoSpendTravelsWithoutAHold(t *testing.T) {
	for name, grant := range map[string]*object.LimitGrant{
		"no plan":             nil,
		"a plan holding none": {Plan: "max-20x", Spend: 0, Settle: func(int64) {}},
	} {
		t.Run(name, func(t *testing.T) {
			cooled.forget()
			_, spends := planEnso(t, false, "0")
			body := []byte(`{"model":"enso-pro","messages":[{"role":"user","content":"hi"}]}`)
			c := visit(http.MethodPost, "/v1/chat/completions")
			c.Fiber().Request().SetBody(body)
			Cover(c.Ctx, grant)
			c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-pro", body, false, 0, "acme", nil, false, nil, time.Now())
			drain(t, c)
			if got := spends(); len(got) != 1 || got[0] != "" {
				t.Fatalf("Enso was sent spend %v, want none", got)
			}
		})
	}
}

// The spend travels to a Hanzo family only: when Enso cannot answer and the pool
// does, the pool's vendor is never told what the plan holds.
func TestTheSpendNeverReachesAnotherFamily(t *testing.T) {
	cooled.forget()
	forgetKeys()
	const free = "vendor/big:free"
	var poolSpend []string
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		poolSpend = append(poolSpend, r.Header.Get(spendHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","model":"` + free + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer pool.Close()
	spareFamily(t, pool.URL, free)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer down.Close()
	restore(t, ensoFam)
	ensoFam.providerFn = func() *object.Provider {
		return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: down.URL}
	}
	ensoFam.loaded, ensoFam.fetchedAt = true, time.Now()

	body := []byte(`{"model":"enso-flash","messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	Cover(c.Ctx, &object.LimitGrant{Plan: "max-20x", Spend: 50_000_000, Settle: func(int64) {}})
	c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-flash", body, false, 0, "acme", nil, false, nil, time.Now())
	if len(poolSpend) == 0 {
		t.Fatal("the pool was never asked — this test proves nothing")
	}
	for _, s := range poolSpend {
		if s != "" {
			t.Fatalf("the pool's vendor was sent the plan's spend %q", s)
		}
	}
}

// enso stands up an Enso service that answers every request with handler.
func enso(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	restore(t, ensoFam)
	ensoFam.providerFn = func() *object.Provider {
		return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: srv.URL}
	}
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro", Plan: true}}
	ensoFam.ids = []string{"enso-pro"}
	ensoFam.loaded, ensoFam.fetchedAt = true, time.Now()
}

// drain runs a streamed answer's writer to its end, which is where it settles.
func drain(t *testing.T, c *ApiController) string {
	t.Helper()
	s := toStream()
	if err := c.Fiber().Response().BodyWriteTo(s.w); err != nil {
		t.Fatal(err)
	}
	_ = s.w.Flush()
	return s.buf.String()
}

// pipeCovered sends one covered enso-pro chat request through the family pipe, asked
// whole, and answers what its grant was settled with.
func pipeCovered(t *testing.T, spend int64) []int64 { return pipeCoveredAs(t, spend, "openai") }

// pipeCoveredAs is pipeCovered in dialect: "openai" (a chat completion, which ai asks
// of the family as a stream), "openai-stream" (one the caller streams) or "anthropic"
// (a message, asked whole).
func pipeCoveredAs(t *testing.T, spend int64, dialect string) []int64 {
	t.Helper()
	settled, _ := coveredCall(t, spend, dialect)
	return settled
}

// coveredCall is pipeCoveredAs that also answers what the caller was sent: the body,
// with the status ahead of it when it is not 200.
func coveredCall(t *testing.T, spend int64, dialect string) ([]int64, string) {
	t.Helper()
	settled, sent, _ := planCall(t, &spend, dialect, time.Now())
	return settled, sent
}

// planCall sends one enso-pro request through the family pipe, covered by a grant of spend
// when spend is set, as if it began at start, and answers what settled, what the caller
// was sent, and how long the caller waited for its status: pipeToFamily returning is
// the moment the status and the first bytes can leave.
func planCall(t *testing.T, spend *int64, dialect string, start time.Time) ([]int64, string, time.Duration) {
	t.Helper()
	cooled.forget()
	var mu sync.Mutex
	var settled []int64
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(context.Context, object.UsageEvent) error { return nil })
	t.Cleanup(func() { object.SetUsageRecorder(prev) })
	path, apiPath := "/v1/chat/completions", "chat/completions"
	body := []byte(`{"model":"enso-pro","messages":[{"role":"user","content":"hi"}]}`)
	stream := dialect == "openai-stream"
	if stream {
		dialect = "openai"
	}
	if dialect == "anthropic" {
		path, apiPath = "/v1/messages", "messages"
		body = []byte(`{"model":"enso-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	}
	c := visit(http.MethodPost, path)
	c.Fiber().Request().SetBody(body)
	if spend != nil {
		Cover(c.Ctx, &object.LimitGrant{Plan: "max-20x", Spend: *spend, Settle: func(n int64) { mu.Lock(); settled = append(settled, n); mu.Unlock() }})
	}
	began := time.Now()
	c.pipeToFamily(ensoFam, apiPath, dialect, "enso-pro", body, stream, 0, "acme", &iam.User{Owner: "acme", Name: "ann"}, false, nil, start)
	waited := time.Since(began)
	sent := drain(t, c)
	if st := c.Fiber().Response().StatusCode(); st != http.StatusOK {
		sent = fmt.Sprintf("%d %s", st, sent)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]int64(nil), settled...), sent, waited
}

// charged is what settles add up to, and whether the first of them is all of it.
func charged(got []int64) (sum int64, once bool) {
	for _, n := range got {
		sum += n
	}
	return sum, len(got) > 0 && got[0] == sum
}

// A covered request its family refuses, with nothing served, settles at nothing as
// soon as it ends: its hold never waits out its keeper.
func TestARefusedCoveredRequestSettlesAtNothing(t *testing.T) {
	enso(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request"}}`)
	})
	got := pipeCovered(t, 50_000_000)
	if len(got) == 0 {
		t.Fatal("a refused covered request was never settled")
	}
	for _, n := range got {
		if n != 0 {
			t.Fatalf("settled %v, want nothing charged", got)
		}
	}
}

// Once a paid rung was tried, no later dispatch of the request carries the plan's
// spend: one request buys at most one paid answer. Only a Hanzo family is ever sent
// it, only for a SKU its family lists as plan-capable, and only while the grant holds
// something.
func TestThePlansSpendIsNeverSentTwice(t *testing.T) {
	restore(t, ensoFam)
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro", Plan: true}, "enso-flash": {ID: "enso-flash"}}
	ensoFam.ids = []string{"enso-pro", "enso-flash"}
	ensoFam.loaded, ensoFam.fetchedAt = true, time.Now()
	c := visit(http.MethodPost, "/v1/chat/completions")
	if got := spendFor(c.Ctx, ensoFam, "enso-pro"); got != "" {
		t.Fatalf("no grant sent spend %q", got)
	}
	Cover(c.Ctx, &object.LimitGrant{Plan: "max-20x", Spend: 1_000_000_000, Settle: func(int64) {}})
	if got := spendFor(c.Ctx, ensoFam, "enso-pro"); got != "1.000000000" {
		t.Fatalf("a covered request sent spend %q, want the hold", got)
	}
	if got := spendFor(c.Ctx, ensoFam, "enso-flash"); got != "" {
		t.Fatalf("a SKU its family does not list as plan-capable was sent spend %q", got)
	}
	if got := spendFor(c.Ctx, freeFamily(), "vendor/big:free"); got != "" {
		t.Fatalf("a borrowed family was sent spend %q", got)
	}
	markOf(c.Ctx).tried.Store(true)
	if got := spendFor(c.Ctx, ensoFam, "enso-pro"); got != "" {
		t.Fatalf("after a paid rung was tried the request still sends spend %q", got)
	}
}

// A family says which SKUs it opens plan rungs for in its listing, and ai says it
// reads the cost a committed answer states (TE: trailers) with every spend it sends.
// A SKU listed without it, as every listing from a family that predates the committed
// answer is, is sent no spend: a plan opens no paid rung until both sides run it.
func TestASpendTravelsOnlyBetweenSidesThatSpeakTheCommittedAnswer(t *testing.T) {
	var got struct{ spend, te string }
	for _, tc := range []struct {
		name            string
		plan            bool
		wantSpend, want string
	}{
		{"a SKU listed as plan-capable", true, "1.000000000", "trailers"},
		{"a SKU listed without it", false, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enso(t, func(w http.ResponseWriter, r *http.Request) {
				got.spend, got.te = r.Header.Get(spendHeader), r.Header.Get("TE")
				committed(w, true, "0.004", freeSSE, "0")
			})
			ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro", Plan: tc.plan}}
			pipeCovered(t, 1_000_000_000)
			if got.spend != tc.wantSpend || got.te != tc.want {
				t.Errorf("Enso was sent spend %q and TE %q, want %q and %q", got.spend, got.te, tc.wantSpend, tc.want)
			}
		})
	}
	var listed struct {
		Data []zenWireModel `json:"data"`
	}
	if err := json.Unmarshal([]byte(`{"data":[{"id":"enso-pro","plan":true},{"id":"enso-flash"}]}`), &listed); err != nil {
		t.Fatal(err)
	}
	if !listed.Data[0].model().Plan || listed.Data[1].model().Plan {
		t.Errorf("listing read as plan %t/%t, want true/false", listed.Data[0].model().Plan, listed.Data[1].model().Plan)
	}
}

// ensoAt points the Enso family at handler and shortens the wait for a family's
// headers to wait, so a test can outlast it.
func ensoAt(t *testing.T, wait time.Duration, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	restore(t, ensoFam)
	ensoFam.providerFn = func() *object.Provider {
		return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: srv.URL}
	}
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro", Plan: true}}
	ensoFam.ids = []string{"enso-pro"}
	ensoFam.loaded, ensoFam.fetchedAt = true, time.Now()
	prev := zenPipeClient
	zenPipeClient = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: wait}}
	t.Cleanup(func() { zenPipeClient = prev })
}

// A dispatch that carried the plan's spend and got no answer back — its headers came
// after ai stopped waiting, or the connection was cut — is the request's one paid
// attempt: no later dispatch carries the spend again. It is charged nothing, since a
// family states a paid rung's rate and bound the moment that rung's upstream accepts.
func TestASpendWithNoAnswerIsTheOnePaidAttempt(t *testing.T) {
	cases := map[string]func(http.ResponseWriter, *http.Request){
		"headers past the wait": func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			committed(w, true, "0.004", paidSSE, "0.0025")
		},
		"connection cut": func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		},
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			var asked, spent atomic.Int32
			ensoAt(t, 100*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
				asked.Add(1)
				if r.Header.Get(spendHeader) != "" {
					spent.Add(1)
				}
				answer(w, r)
			})
			for _, n := range pipeCovered(t, 1_000_000_000) {
				if n != 0 {
					t.Errorf("a dispatch with no answer was charged %d", n)
				}
			}
			if spent.Load() != 1 {
				t.Errorf("enso was sent the spend %d time(s) over %d dispatch(es), want once", spent.Load(), asked.Load())
			}
		})
	}
}

// A dispatch that never carried the spend and got no answer charges the plan
// nothing: no paid rung could have been asked.
func TestNoSpendNoAnswerSettlesNothing(t *testing.T) {
	ensoAt(t, 100*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	for _, n := range pipeCovered(t, 0) {
		if n != 0 {
			t.Fatalf("a request that sent no spend was charged %d", n)
		}
	}
}

// A committed answer cut before its end states no cost: it is charged the most its
// family said the request can cost once a paid rung's upstream accepted it — the paid
// marker on a stream, a tab ahead of a whole answer — and nothing before that.
// A family that states no bound is charged at most unstatedNanos, never what its family
// has left.
func TestACutAnswerSettlesItsBoundOnceAPaidRungAccepted(t *testing.T) {
	const left = 10_000_000_000 // $10 the family has left this period
	words := `data: {"id":"x","choices":[{"delta":{"content":"ok"}}]}` + "\n\n"
	for _, tc := range []struct {
		name, dialect string
		answer        http.HandlerFunc
		want          int64
	}{
		{"a stream cut after the paid marker", "openai", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, true, "0.004", ": keep-alive\n\n", ": paid\n\n", words)
		}, 4_000_000},
		{"a stream cut before any paid rung accepted", "openai", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, true, "0.004", ": keep-alive\n\n", words)
		}, 0},
		{"a caller's stream cut after the paid marker", "openai-stream", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, true, "0.004", ": keep-alive\n\n", ": paid\n\n", words)
		}, 4_000_000},
		{"a caller's stream cut before any paid rung accepted", "openai-stream", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, true, "0.004", ": keep-alive\n\n", words)
		}, 0},
		{"a whole answer cut after a paid rung accepted", "anthropic", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, false, "0.004", " \t", `{"id":"msg_1",`)
		}, 4_000_000},
		{"a bound past what is left", "openai", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, true, "75", ": paid\n\n", words)
		}, left},
		{"no bound stated", "openai", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, true, "", ": paid\n\n", words)
		}, unstatedNanos},
		{"a malformed bound", "openai", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, true, "lots", ": paid\n\n", words)
		}, unstatedNanos},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enso(t, tc.answer)
			got := pipeCoveredAs(t, left, tc.dialect)
			if sum, once := charged(got); sum != tc.want || (tc.want > 0 && !once) {
				t.Errorf("settled %v, want %d first and once", got, tc.want)
			}
		})
	}
}

// A committed answer that ends whole is charged the cost its family states, never past
// what the family has left, whatever the paid marker said; one that ends whole with no
// stated cost is charged as a cut one.
func TestAWholeAnswerSettlesItsStatedCost(t *testing.T) {
	const left = 1_000_000_000 // $1
	for _, tc := range []struct {
		name, dialect, body, cost string
		want                      int64
	}{
		{"a paid stream", "openai", paidSSE, "0.0025", 2_500_000},
		{"a free stream", "openai", freeSSE, "0", 0},
		{"a caller's paid stream", "openai-stream", paidSSE, "0.0025", 2_500_000},
		{"a cost past what is left", "openai", paidSSE, "7.5", left},
		{"a paid whole answer", "anthropic", ` {"id":"msg_1","type":"message","role":"assistant","model":"enso-pro","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5}}`, "0.0001", 100_000},
		{"a free whole answer", "anthropic", ` {"id":"msg_1","type":"message","role":"assistant","model":"enso-pro","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5}}`, "0", 0},
		{"a paid stream that states no cost", "openai", paidSSE, "", 4_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enso(t, func(w http.ResponseWriter, r *http.Request) {
				committed(w, tc.dialect != "anthropic", "0.004", tc.body, tc.cost)
			})
			got := pipeCoveredAs(t, left, tc.dialect)
			if sum, once := charged(got); sum != tc.want || (tc.want > 0 && !once) {
				t.Errorf("settled %v, want %d first and once", got, tc.want)
			}
		})
	}
}

// freePool stands up the free pool answering every request with "from-pool", in the
// shape it was asked for, and counts the requests it was sent.
func freePool(t *testing.T) *atomic.Int32 {
	t.Helper()
	cooled.forget()
	forgetKeys()
	var asked atomic.Int32
	const free = "vendor/big:free"
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg_p","type":"message","role":"assistant","model":"`+free+`","content":[{"type":"text","text":"from-pool"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
		case strings.Contains(string(b), `"stream":true`):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"id":"p","model":"`+free+`","choices":[{"delta":{"content":"from-pool"}}]}`+"\n\n"+
				`data: {"id":"p","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`+"\n\ndata: [DONE]\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"p","model":"`+free+`","choices":[{"index":0,"message":{"role":"assistant","content":"from-pool"},"finish_reason":"stop"}]}`)
		}
	}))
	t.Cleanup(pool.Close)
	spareFamily(t, pool.URL, free)
	return &asked
}

// A committed answer that fails before it says anything is a refusal like any other
// family's: it falls to the free pool, exactly as it would for a caller no plan
// covers, and it is charged what its family states it cost — nothing when no paid
// rung accepted it, whatever bound it was committed under. Late or whole, it is the
// same: a covered caller never sees an error a caller with no plan would not.
func TestACommittedFailureFallsToThePool(t *testing.T) {
	prev := openWait
	openWait = 50 * time.Millisecond
	t.Cleanup(func() { openWait = prev })
	fail := `data: {"error":{"message":"this model is temporarily unavailable","code":503}}` + "\n\n"
	late := func(w http.ResponseWriter, sse bool, tail, cost string) {
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.Header().Set(costBoundHeader, "0.75")
		w.Header().Set("Trailer", costHeader)
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 6; i++ { // the family's keep-alives while it walks, past openWait
			if sse {
				_, _ = io.WriteString(w, ": keep-alive\n\n")
			} else {
				_, _ = io.WriteString(w, " ")
			}
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = io.WriteString(w, tail)
		w.Header().Set(costHeader, cost)
	}
	for _, tc := range []struct {
		name, dialect string
		answer        http.HandlerFunc
		want          int64
	}{
		{"a stream that fails before any paid rung", "openai", func(w http.ResponseWriter, r *http.Request) {
			committed(w, true, "0.75", ": keep-alive\n\n"+fail, "0")
		}, 0},
		{"a caller's stream that fails before any paid rung", "openai-stream", func(w http.ResponseWriter, r *http.Request) {
			committed(w, true, "0.75", ": keep-alive\n\n"+fail, "0")
		}, 0},
		{"a stream that fails past openWait", "openai-stream", func(w http.ResponseWriter, r *http.Request) {
			late(w, true, fail, "0")
		}, 0},
		{"a stream that ends empty", "openai", func(w http.ResponseWriter, r *http.Request) {
			committed(w, true, "0.75", ": keep-alive\n\ndata: [DONE]\n\n", "0")
		}, 0},
		{"a paid rung that accepted, then failed", "openai-stream", func(w http.ResponseWriter, r *http.Request) {
			committed(w, true, "0.75", ": paid\n\n"+fail, "0.004")
		}, 4_000_000},
		{"a paid rung that accepted, then failed, its cost lost", "openai-stream", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, true, "0.004", ": paid\n\n", fail)
		}, 4_000_000},
		{"a whole answer that is an error object", "anthropic", func(w http.ResponseWriter, r *http.Request) {
			late(w, false, `{"type":"error","error":{"type":"api_error","message":"this model is temporarily unavailable"}}`, "0")
		}, 0},
		{"a whole answer that is an error object, a paid rung having failed", "anthropic", func(w http.ResponseWriter, r *http.Request) {
			committed(w, false, "0.75", ` {"error":{"message":"the model returned an unusable answer","type":"zen_error","code":502}}`, "0.004")
		}, 4_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := freePool(t)
			enso(t, tc.answer)
			got, sent := coveredCall(t, 1_000_000_000, tc.dialect)
			if !strings.Contains(sent, "from-pool") || strings.Contains(sent, "temporarily unavailable") || strings.Contains(sent, "unusable") {
				t.Errorf("the caller was sent %q (pool asked %d), want the free pool's answer", sent, asked.Load())
			}
			if sum, once := charged(got); sum != tc.want || (tc.want > 0 && !once) {
				t.Errorf("settled %v, want %d first and once", got, tc.want)
			}
		})
	}
}

// A committed answer that has not begun by the time ai stops waiting for a family is
// abandoned exactly as a family that sent no headers would be: the family is hung up
// on, which stops its walk before any paid rung, the free pool answers, and nothing is
// charged. Once a paid rung accepted, ai waits for its answer however long it takes.
func TestACommittedAnswerThatNeverBeginsFallsToThePool(t *testing.T) {
	prev := commitWait
	commitWait = 100 * time.Millisecond
	t.Cleanup(func() { commitWait = prev })
	hungUp := make(chan struct{}, 1)
	asked := freePool(t)
	enso(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set(costBoundHeader, "0.75")
		w.Header().Set("Trailer", costHeader)
		w.WriteHeader(http.StatusOK)
		for {
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				hungUp <- struct{}{}
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				hungUp <- struct{}{}
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	got, sent := coveredCall(t, 1_000_000_000, "openai-stream")
	if !strings.Contains(sent, "from-pool") {
		t.Errorf("the caller was sent %q (pool asked %d), want the free pool's answer", sent, asked.Load())
	}
	if sum, _ := charged(got); sum != 0 {
		t.Errorf("settled %v, want nothing", got)
	}
	select {
	case <-hungUp:
	case <-time.After(2 * time.Second):
		t.Error("the family was never hung up on: its walk goes on")
	}

	slow := `data: {"id":"x","choices":[{"delta":{"content":"paid words"}}]}` + "\n\n"
	enso(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set(costBoundHeader, "0.75")
		w.Header().Set("Trailer", costHeader)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": paid\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond) // the paid rung thinks past commitWait
		_, _ = io.WriteString(w, slow+"data: [DONE]\n\n")
		w.Header().Set(costHeader, "0.002")
	})
	got, sent = coveredCall(t, 1_000_000_000, "openai-stream")
	if !strings.Contains(sent, "paid words") {
		t.Errorf("the caller was sent %q, want the paid rung's answer", sent)
	}
	if sum, _ := charged(got); sum != 2_000_000 {
		t.Errorf("settled %v, want the stated 2000000", got)
	}
}

// A committed refusal the caller caused — a request no vendor would serve — reaches
// the caller with its status, as it would with no plan, and charges nothing.
func TestACommittedRefusalTheCallerCausedReachesTheCaller(t *testing.T) {
	asked := freePool(t)
	enso(t, func(w http.ResponseWriter, r *http.Request) {
		committed(w, true, "0.75", ": keep-alive\n\n"+`data: {"error":{"message":"messages must not be empty","code":400}}`+"\n\n", "0")
	})
	got, sent := coveredCall(t, 1_000_000_000, "openai")
	if !strings.HasPrefix(sent, "400 ") || asked.Load() != 0 {
		t.Errorf("the caller was sent %q and the pool asked %d times; want the 400 and no pool", sent, asked.Load())
	}
	if sum, _ := charged(got); sum != 0 {
		t.Errorf("settled %v, want nothing", got)
	}
}

// slowCommit answers a committed stream that keeps its host waiting for think — its
// family walking its rungs, keep-alives meanwhile — then ends with tail and states
// cost (none when empty). paid writes the paid marker first.
func slowCommit(think time.Duration, paid bool, tail, cost string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set(costBoundHeader, "0.004")
		w.Header().Set("Trailer", costHeader)
		w.WriteHeader(http.StatusOK)
		if paid {
			_, _ = io.WriteString(w, ": paid\n\n")
		}
		for end := time.Now().Add(think); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, tail)
		if cost != "" {
			w.Header().Set(costHeader, cost)
		}
	}
}

// A covered caller is answered on the clock an uncovered one is: its status and
// keep-alives go out once openWait passes, however long the committed answer takes
// to begin, and the answer follows in the same response. With no free route to answer
// a refusal from, the request is held instead, so a refusal still goes back to its
// route's other providers.
func TestACoveredCallerIsAnsweredOnTheUncoveredClock(t *testing.T) {
	prev := openWait
	openWait = 50 * time.Millisecond
	t.Cleanup(func() { openWait = prev })
	const think = 400 * time.Millisecond
	words := `data: {"id":"x","model":"enso-pro","choices":[{"delta":{"content":"paid words"}}]}` + "\n\ndata: [DONE]\n\n"
	for _, d := range []string{"openai-stream", "openai"} {
		t.Run(d, func(t *testing.T) {
			freePool(t)
			enso(t, slowCommit(think, true, words, "0.002"))
			spend := int64(1_000_000_000)
			got, sent, waited := planCall(t, &spend, d, time.Now())
			if waited >= think/2 {
				t.Errorf("the covered caller waited %v for its status, past openWait", waited.Round(time.Millisecond))
			}
			if !strings.Contains(sent, "paid words") {
				t.Errorf("the caller was sent %q, want the answer", sent)
			}
			if sum, once := charged(got); sum != 2_000_000 || !once {
				t.Errorf("settled %v, want the stated 2000000 once", got)
			}
		})
	}
}

// A committed answer that refuses after the caller's answer opened — its family's
// rungs all failed, or a paid rung accepted and then failed — is answered from the free
// pool inside that open answer, never with the error, and charged what its family
// states: nothing when no paid rung accepted it. One ai stops waiting for (commitWait)
// is hung up on and answered the same way, whenever the request began.
func TestARefusalAfterTheAnswerOpenedIsAnsweredFromThePool(t *testing.T) {
	prevOpen, prevCommit := openWait, commitWait
	openWait, commitWait = 50*time.Millisecond, 2*time.Second
	t.Cleanup(func() { openWait, commitWait = prevOpen, prevCommit })
	fail := `data: {"error":{"message":"this model is temporarily unavailable","code":503}}` + "\n\n"
	for _, tc := range []struct {
		name, dialect string
		answer        http.HandlerFunc
		wait          time.Duration
		want          int64
	}{
		{"every rung failed", "openai-stream", slowCommit(300*time.Millisecond, false, fail, "0"), 0, 0},
		{"every rung failed, asked whole", "openai", slowCommit(300*time.Millisecond, false, fail, "0"), 0, 0},
		{"a paid rung accepted, then failed", "openai-stream", slowCommit(300*time.Millisecond, true, fail, "0.004"), 0, 4_000_000},
		{"ai stopped waiting before any paid rung", "openai-stream", slowCommit(time.Hour, false, "", ""), 200 * time.Millisecond, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wait > 0 {
				commitWait = tc.wait
				t.Cleanup(func() { commitWait = 2 * time.Second })
			}
			asked := freePool(t)
			enso(t, tc.answer)
			spend := int64(1_000_000_000)
			// The request began long ago: the pool still answers inside an open answer.
			got, sent, waited := planCall(t, &spend, tc.dialect, time.Now().Add(-3*time.Minute))
			if waited >= 200*time.Millisecond {
				t.Errorf("the covered caller waited %v for its status, past openWait", waited.Round(time.Millisecond))
			}
			if !strings.Contains(sent, "from-pool") || strings.Contains(sent, "temporarily unavailable") {
				t.Errorf("the caller was sent %q (pool asked %d), want the free pool's answer", sent, asked.Load())
			}
			if sum, once := charged(got); sum != tc.want || (tc.want > 0 && !once) {
				t.Errorf("settled %v, want %d", got, tc.want)
			}
		})
	}
}

// A covered request its family refuses without committing, handed back to the route's
// other providers, settles its grant before it goes: what it owes, else nothing. No
// hold outlives it.
func TestARefusalHandedBackSettlesTheGrant(t *testing.T) {
	enso(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"this model is temporarily unavailable","code":503}}`)
	})
	for _, d := range []string{"openai-stream", "openai"} {
		spend := int64(1_000_000_000)
		got, _, _ := planCall(t, &spend, d, time.Now())
		if len(got) == 0 {
			t.Errorf("%s: the request was handed back with its grant unsettled", d)
		}
	}
}

// A whole committed answer cut before it ends is charged its bound only when a paid
// rung accepted it, which a whole answer says with a tab ahead of its body.
func TestAWholeCommitIsPaidOnlyPastItsTab(t *testing.T) {
	for _, tc := range []struct {
		name, lead string
		want       int64
	}{
		{"cut before any paid rung", " ", 0},
		{"cut after a paid rung accepted", " \t", 4_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			freePool(t)
			enso(t, func(w http.ResponseWriter, r *http.Request) {
				cutAfter(w, false, "0.004", tc.lead, " ")
			})
			got := pipeCoveredAs(t, 1_000_000_000, "anthropic")
			if sum, _ := charged(got); sum != tc.want {
				t.Errorf("settled %v, want %d", got, tc.want)
			}
		})
	}
}

// A covered request with no free route to answer a refusal from is held until its
// committed answer begins or refuses, and a refusal goes back to its route's other
// providers, as an uncovered caller's does.
func TestACoveredRequestWithNoFreeRouteIsHeld(t *testing.T) {
	prev := openWait
	openWait = 50 * time.Millisecond
	t.Cleanup(func() { openWait = prev })
	restore(t, freeFamily())
	freeFamily().spares, freeFamily().loaded, freeFamily().fetchedAt = nil, true, time.Now()
	fail := `data: {"error":{"message":"this model is temporarily unavailable","code":503}}` + "\n\n"
	enso(t, slowCommit(200*time.Millisecond, false, fail, "0"))
	c := visit(http.MethodPost, "/v1/chat/completions")
	body := []byte(`{"model":"enso-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c.Fiber().Request().SetBody(body)
	var got []int64
	Cover(c.Ctx, &object.LimitGrant{Plan: "max-20x", Spend: 1_000_000_000, Settle: func(n int64) { got = append(got, n) }})
	out := c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-pro", body, true, 0, "acme", nil, false, nil, time.Now())
	if out == nil {
		t.Fatalf("the refusal was not handed back to the route; the caller was sent %q", drain(t, c))
	}
	if sum, _ := charged(got); sum != 0 {
		t.Errorf("settled %v, want nothing", got)
	}
}

// heard records when each write of a streamed answer reached the caller.
type heard struct {
	mu    sync.Mutex
	buf   strings.Builder
	times []time.Time
}

func (g *heard) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.times = append(g.times, time.Now())
	g.buf.Write(p)
	return len(p), nil
}

// silence is the longest gap between two writes the caller saw.
func (g *heard) silence() time.Duration {
	var m time.Duration
	for i := 1; i < len(g.times); i++ {
		m = max(m, g.times[i].Sub(g.times[i-1]))
	}
	return m
}

// openCall sends one covered enso-pro request whose committed answer opens (openWait
// and heartbeat shortened) and answers what the caller got and the longest silence.
func openCall(t *testing.T, path, apiPath, dialect string, body []byte, stream bool) (string, time.Duration) {
	t.Helper()
	cooled.forget()
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(context.Context, object.UsageEvent) error { return nil })
	t.Cleanup(func() { object.SetUsageRecorder(prev) })
	c := visit(http.MethodPost, path)
	c.Fiber().Request().SetBody(body)
	Cover(c.Ctx, &object.LimitGrant{Plan: "max-20x", Spend: 1_000_000_000, Settle: func(int64) {}})
	c.pipeToFamily(ensoFam, apiPath, dialect, "enso-pro", body, stream, 0, "acme", &iam.User{Owner: "acme", Name: "ann"}, false, nil, time.Now())
	g := &heard{}
	_ = c.Fiber().Response().BodyWriteTo(g)
	return g.buf.String(), g.silence()
}

// opened shortens openWait and heartbeat so a committed answer opens, and keeps alive,
// within a test's time.
func opened(t *testing.T) {
	t.Helper()
	prevOpen, prevBeat := openWait, heartbeat
	openWait, heartbeat = 50*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { openWait, heartbeat = prevOpen, prevBeat })
}

// poolSays stands up the free pool answering every request with handler.
func poolSays(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	cooled.forget()
	forgetKeys()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	spareFamily(t, srv.URL, "vendor/big:free")
}

var (
	chatWhole  = []byte(`{"model":"enso-pro","messages":[{"role":"user","content":"hi"}]}`)
	chatStream = []byte(`{"model":"enso-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	msgStream  = []byte(`{"model":"enso-pro","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	unavail    = `data: {"error":{"message":"this model is temporarily unavailable","code":503}}` + "\n\n"
)

// An open answer the free pool cannot fill ends in the caller's dialect's error: a
// JSON error frame on a chat stream, an Anthropic error event on a message stream, an
// error object on a whole answer — never Go's words, never an empty completion.
func TestAnOpenAnswerThePoolCannotFillEndsInTheDialectsError(t *testing.T) {
	opened(t)
	prev := commitWait
	commitWait = 300 * time.Millisecond
	t.Cleanup(func() { commitWait = prev })
	poolSays(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"busy"}}`)
	})
	for _, tc := range []struct {
		name, path, api, dialect string
		body                     []byte
		stream                   bool
		check                    func(string) bool
	}{
		{"chat stream", "/v1/chat/completions", "chat/completions", "openai", chatStream, true, func(s string) bool {
			return strings.Contains(s, `data: {"error":{`) && !strings.Contains(s, "data: the family")
		}},
		{"chat whole", "/v1/chat/completions", "chat/completions", "openai", chatWhole, false, func(s string) bool {
			return strings.HasPrefix(strings.TrimSpace(s), `{"error":{`)
		}},
		{"message stream", "/v1/messages", "messages", "anthropic", msgStream, true, func(s string) bool {
			return strings.Contains(s, "event: error\ndata: {") && strings.Contains(s, `"type":"error"`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enso(t, slowCommit(time.Hour, false, "", ""))
			sent, _ := openCall(t, tc.path, tc.api, tc.dialect, tc.body, tc.stream)
			if !tc.check(sent) {
				t.Errorf("the caller was sent %q", sent)
			}
		})
	}
}

// The free pool's answer given whole, inside an open answer, is the stream it adds up
// to in the caller's dialect: each tool call its own, a whole Anthropic message as its
// events. A commit's own event lines ahead of its refusal never reach the caller.
func TestAWholePoolAnswerIsStreamedInTheCallersDialect(t *testing.T) {
	opened(t)
	whole := `{"id":"p","model":"vendor/big:free","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[` +
		`{"id":"a","type":"function","function":{"name":"read","arguments":"{\"f\":1}"}},` +
		`{"id":"b","type":"function","function":{"name":"write","arguments":"{\"g\":2}"}}]}}]}`
	poolSays(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, whole)
	})
	enso(t, slowCommit(200*time.Millisecond, false, unavail, "0"))
	sent, _ := openCall(t, "/v1/chat/completions", "chat/completions", "openai", chatWhole, false)
	if strings.Contains(sent, "readwrite") || !strings.Contains(sent, `"name":"read"`) || !strings.Contains(sent, `"name":"write"`) {
		t.Errorf("the two tool calls arrived as %s", strings.TrimSpace(sent))
	}

	freePool(t) // answers /messages with a whole Anthropic message
	fail := "event: error\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"this model is temporarily unavailable"}}` + "\n\n"
	enso(t, slowCommit(200*time.Millisecond, false, fail, "0"))
	sent, _ = openCall(t, "/v1/messages", "messages", "anthropic", msgStream, true)
	if !strings.Contains(sent, "from-pool") || strings.Contains(sent, "event: error") || !strings.Contains(sent, "event: message_stop") {
		t.Errorf("the caller was sent %q", sent)
	}
}

// While the free pool works on an open stream's answer, the caller keeps hearing
// keep-alives.
func TestThePoolKeepsTheCallerAlive(t *testing.T) {
	opened(t)
	const slow = 600 * time.Millisecond
	poolSays(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(slow)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"p","choices":[{"delta":{"content":"from-pool"}}]}`+"\n\ndata: [DONE]\n\n")
	})
	enso(t, slowCommit(200*time.Millisecond, false, unavail, "0"))
	sent, gap := openCall(t, "/v1/chat/completions", "chat/completions", "openai", chatStream, true)
	if !strings.Contains(sent, "from-pool") || gap >= slow/2 {
		t.Errorf("the caller heard nothing for %v (pool answered: %t)", gap.Round(time.Millisecond), strings.Contains(sent, "from-pool"))
	}
}
