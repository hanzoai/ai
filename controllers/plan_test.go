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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro"}}
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
			if len(events) != 1 || !events[0].Plan || events[0].Allowance != "" {
				t.Fatalf("usage events %+v, want one marked as the plan's, counting no allowance", events)
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
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro"}}
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
	cooled.forget()
	var mu sync.Mutex
	var settled []int64
	grant := &object.LimitGrant{Plan: "max-20x", Spend: spend, Settle: func(n int64) { mu.Lock(); settled = append(settled, n); mu.Unlock() }}
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
	Cover(c.Ctx, grant)
	c.pipeToFamily(ensoFam, apiPath, dialect, "enso-pro", body, stream, 0, "acme", &iam.User{Owner: "acme", Name: "ann"}, false, nil, time.Now())
	drain(t, c)
	mu.Lock()
	defer mu.Unlock()
	return append([]int64(nil), settled...)
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
// it, and only while the grant holds something.
func TestThePlansSpendIsNeverSentTwice(t *testing.T) {
	c := visit(http.MethodPost, "/v1/chat/completions")
	if got := spendFor(c.Ctx, ensoFam); got != "" {
		t.Fatalf("no grant sent spend %q", got)
	}
	Cover(c.Ctx, &object.LimitGrant{Plan: "max-20x", Spend: 1_000_000_000, Settle: func(int64) {}})
	if got := spendFor(c.Ctx, ensoFam); got != "1.000000000" {
		t.Fatalf("a covered request sent spend %q, want the hold", got)
	}
	if got := spendFor(c.Ctx, freeFamily()); got != "" {
		t.Fatalf("a borrowed family was sent spend %q", got)
	}
	markOf(c.Ctx).tried.Store(true)
	if got := spendFor(c.Ctx, ensoFam); got != "" {
		t.Fatalf("after a paid rung was tried the request still sends spend %q", got)
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
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro"}}
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
// marker on a stream, the commit itself on a whole answer — and nothing before that.
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
		{"a whole answer cut after its commit", "anthropic", func(w http.ResponseWriter, r *http.Request) {
			cutAfter(w, false, "0.004", " ", `{"id":"msg_1",`)
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
