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
			if len(settled) != 1 || settled[0] != tc.want {
				t.Fatalf("settled %v, want [%d]", settled, tc.want)
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
