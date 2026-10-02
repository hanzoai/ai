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

// The rate a family states prices the tokens exactly, rounded up to the nano; a rung
// that states no cache price charges a cached token at its input rate, as the family
// does; a malformed header is no rate at all.
func TestTheStatedRatePricesTheTokens(t *testing.T) {
	h := http.Header{}
	h.Set(costRateHeader, "0.5,1,0")
	r := readCostRate(h)
	if r == nil {
		t.Fatal("a well-formed rate was not read")
	}
	// 1,000 fresh at $0.50/M + 2,000 out at $1/M = $0.0025; 1,000 cached at the input rate.
	if got := r.nanos(1000, 0, 2000); got != 2_500_000 {
		t.Errorf("nanos = %d, want 2500000", got)
	}
	if got := r.nanos(0, 1000, 0); got != 500_000 {
		t.Errorf("cached nanos = %d, want 500000 (the input rate)", got)
	}
	h.Set(costRateHeader, "0.0015,0,0")
	if got := readCostRate(h).nanos(1, 0, 0); got != 2 {
		t.Errorf("one token at $0.0015/M = 1.5 nano, rounded up to 2; got %d", got)
	}
	for _, bad := range []string{"", "1,2", "a,b,c", "-1,1,1", "1,1,1,1"} {
		h.Set(costRateHeader, bad)
		if readCostRate(h) != nil {
			t.Errorf("%q read as a rate", bad)
		}
	}
	var none *costRate
	if none.nanos(1000, 1000, 1000) != 0 {
		t.Error("no rate (a free rung) costs something")
	}
	if got := spendUSD(50_000_000); got != "0.050000000" {
		t.Errorf("spendUSD = %q", got)
	}
}

// planEnso stands up an Enso service that records the spend each request carried and
// answers with the given cost rate (none for a free rung).
func planEnso(t *testing.T, rate string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var spends []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		spends = append(spends, r.Header.Get(spendHeader))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(servedHeader, "enso-pro")
		if rate != "" {
			w.Header().Set(costRateHeader, rate)
		}
		w.Header().Set(freeHeader, "true")
		_, _ = w.Write([]byte(`{"id":"1","model":"enso-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1000,"completion_tokens":2000,"total_tokens":3000}}`))
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
// the plan at the rate Enso states for the paid rung that answered: the usage record
// says the plan covered it, so no wallet is debited and no free allowance counted.
// A free rung's answer states no rate and settles nothing.
func TestAPlanCoveredFamilyCallCarriesSpendAndSettlesAtTheRate(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate string
		want int64
	}{
		{"a paid rung answered", "0.5,1,0", 2_500_000},
		{"a free rung answered", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cooled.forget()
			_, spends := planEnso(t, tc.rate)

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
			_, spends := planEnso(t, "")
			body := []byte(`{"model":"enso-pro","messages":[{"role":"user","content":"hi"}]}`)
			c := visit(http.MethodPost, "/v1/chat/completions")
			c.Fiber().Request().SetBody(body)
			Cover(c.Ctx, grant)
			c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-pro", body, false, 0, "acme", nil, false, nil, time.Now())
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

// enso stands up an Enso service that answers status with body and, when rate is
// set, states that a paid rung served at that rate.
func enso(t *testing.T, status int, rate, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if rate != "" {
			w.Header().Set(costRateHeader, rate)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	restore(t, ensoFam)
	ensoFam.providerFn = func() *object.Provider {
		return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: srv.URL}
	}
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro"}}
	ensoFam.ids = []string{"enso-pro"}
	ensoFam.loaded, ensoFam.fetchedAt = true, time.Now()
}

// pipeCovered sends one covered enso-pro request through the family pipe and
// answers what its grant was settled with.
func pipeCovered(t *testing.T, spend int64) []int64 {
	t.Helper()
	cooled.forget()
	var mu sync.Mutex
	var settled []int64
	grant := &object.LimitGrant{Plan: "max-20x", Spend: spend, Settle: func(n int64) { mu.Lock(); settled = append(settled, n); mu.Unlock() }}
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(context.Context, object.UsageEvent) error { return nil })
	t.Cleanup(func() { object.SetUsageRecorder(prev) })
	body := []byte(`{"model":"enso-pro","messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	Cover(c.Ctx, grant)
	c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-pro", body, false, 0, "acme", &iam.User{Owner: "acme", Name: "ann"}, false, nil, time.Now())
	mu.Lock()
	defer mu.Unlock()
	return append([]int64(nil), settled...)
}

// A paid rung's answer that reports no usage settles the whole hold: what it cost is
// unknown, so the budget is charged the most it could have been.
func TestAPaidAnswerWithoutUsageSettlesTheWholeHold(t *testing.T) {
	enso(t, http.StatusOK, "0.5,1,0", `{"id":"1","model":"enso-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	if got := pipeCovered(t, 50_000_000); len(got) == 0 || got[0] != 50_000_000 {
		t.Fatalf("settled %v, want the whole 50000000 hold first", got)
	}
}

// A covered request its family refuses, with nothing served, settles at nothing as
// soon as it ends: its hold never waits out its keeper.
func TestARefusedCoveredRequestSettlesAtNothing(t *testing.T) {
	enso(t, http.StatusBadRequest, "", `{"error":{"message":"bad request"}}`)
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

// A family refusal that states a paid rung's rate is a paid answer the family would
// not relay: what it cost is unknown, so the plan is charged the whole hold, never
// nothing, whatever the status.
func TestARefusalCarryingThePaidRateSettlesTheWholeHold(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusInternalServerError, http.StatusOK} {
		body := `{"error":{"message":"the model returned an unusable answer","type":"zen_error","code":502}}`
		if status == http.StatusOK {
			body = `{"id":"1","model":"enso-pro","choices":[]}`
		}
		enso(t, status, "8.00,40.00,0.40", body)
		got := pipeCovered(t, 1_000_000_000)
		charged := int64(0)
		for _, n := range got {
			charged += n
		}
		if charged != 1_000_000_000 {
			t.Errorf("status %d with the paid rate settled %v, want the whole hold once", status, got)
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
// after ai stopped waiting, or the connection was cut — may have bought a paid
// answer: the plan is charged the whole hold, once, and no later dispatch of the
// request carries the spend again.
func TestASpendWithNoAnswerSettlesTheWholeHold(t *testing.T) {
	cases := map[string]func(http.ResponseWriter, *http.Request){
		"headers past the wait": func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(costRateHeader, "8.00,40.00,0.40")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"1","model":"enso-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":8000}}`))
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
			got := pipeCovered(t, 1_000_000_000)
			charged := int64(0)
			for _, n := range got {
				charged += n
			}
			if len(got) == 0 || got[0] != 1_000_000_000 || charged != 1_000_000_000 {
				t.Errorf("settled %v, want the whole 1000000000 hold first and once", got)
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
