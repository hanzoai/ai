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
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// billed sends one enso-pro request through the family pipe, its kinds marked by mark,
// covered by a paid plan's grant, the family answering from a rung of kind and fast
// as given; it answers what the engine was told, what the plan settled, and the usage
// event the host was handed.
func billed(t *testing.T, kind string, fast bool, mark func(*ApiController)) (http.Header, []int64, object.UsageEvent) {
	t.Helper()
	cooled.forget()
	var mu sync.Mutex
	var told http.Header
	enso(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		told = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set(classHeader, kind)
		if kind == kindFree {
			w.Header().Set(freeHeader, "true")
		}
		if fast {
			w.Header().Set(fastHeader, "true")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"id":"x","model":"enso-pro","choices":[{"delta":{"content":"ok"}}]}`+"\n\n"+
			`data: {"id":"x","choices":[],"usage":{"prompt_tokens":1000000,"completion_tokens":0}}`+"\n\ndata: [DONE]\n\n")
	})
	pro := zenTier{MaxCtx: 1_000_000, In: decimal.MustParse("3.30"), Out: decimal.MustParse("16.50")}
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro", Plan: true, Base: pro, Tiers: []zenTier{pro}}}
	var ev []object.UsageEvent
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, e object.UsageEvent) error {
		mu.Lock()
		ev = append(ev, e)
		mu.Unlock()
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	body := []byte(`{"model":"enso-pro","messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	var settled []int64
	Cover(c.Ctx, &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Spend: 50_000_000_000, Settle: func(n int64) { mu.Lock(); settled = append(settled, n); mu.Unlock() }})
	mark(c)
	c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-pro", body, false, 0, "acme", &iam.User{Owner: "acme", Name: "ann"}, false, nil, time.Now())
	_ = drain(t, c)
	mu.Lock()
	defer mu.Unlock()
	if len(ev) != 1 {
		t.Fatalf("%d usage events, want 1", len(ev))
	}
	return told, append([]int64(nil), settled...), ev[0]
}

// BILLING FOLLOWS THE RUNG THAT SERVED. Our credits bill the plan; a prepaid rung
// bills the payer's cash at list whatever the plan; fast mode bills its multiple of
// the price from cash; a request nothing pays for bills nothing, whichever rung
// answered. The engine is told the kinds the gate gave.
func TestBillingFollowsTheKindThatServed(t *testing.T) {
	paying := func(c *ApiController) { Kinds(c.Ctx, KindsPaying, false, "enso-pro") }

	told, settled, e := billed(t, kindOurs, false, paying)
	if told.Get(classesHeader) != KindsPaying || told.Get(fastHeader) != "" {
		t.Errorf("the engine was told kinds %q fast %q", told.Get(classesHeader), told.Get(fastHeader))
	}
	if !e.Plan || e.Cash || !strings.HasPrefix(e.USD, "3.30") || sum(settled) == 0 {
		t.Errorf("ours: plan %v cash %v usd %q settled %v, want the plan billed 3.3", e.Plan, e.Cash, e.USD, settled)
	}

	_, settled, e = billed(t, kindPremium, false, paying)
	if e.Plan || !e.Cash || !strings.HasPrefix(e.USD, "3.30") || len(settled) == 0 || sum(settled) != 0 {
		t.Errorf("premium: plan %v cash %v usd %q settled %v, want the wallet's cash billed 3.3 and the plan nothing", e.Plan, e.Cash, e.USD, settled)
	}

	told, settled, e = billed(t, kindPremium, true, func(c *ApiController) { Kinds(c.Ctx, KindsFast, true, "enso-pro") })
	if told.Get(classesHeader) != KindsFast || told.Get(fastHeader) != "1" {
		t.Errorf("fast: the engine was told kinds %q fast %q", told.Get(classesHeader), told.Get(fastHeader))
	}
	if e.Plan || !e.Cash || !strings.HasPrefix(e.USD, "9.90") || sum(settled) != 0 {
		t.Errorf("fast on enso-pro: plan %v cash %v usd %q settled %v, want 3x from cash, 9.9", e.Plan, e.Cash, e.USD, settled)
	}

	_, _, e = billed(t, kindOurs, false, func(c *ApiController) { Free(c.Ctx, "enso-auto") })
	if !strings.HasPrefix(e.USD, "0") {
		t.Errorf("a request nothing pays for, served on our credits, billed %q", e.USD)
	}

	told, _, _ = billed(t, kindFree, false, func(*ApiController) {})
	if told.Get(classesHeader) != KindsFree {
		t.Errorf("an unmarked request told the engine %q, want %q", told.Get(classesHeader), KindsFree)
	}
}

// FAST MODE BILLS THE TIER THAT ANSWERED. A router id the caller named resolves to a
// tier before the call, and the tier's multiple applies: enso-auto at max effort is
// enso-ultra, and enso-ultra's fast answer is 4x, not the router's 2x. The converse
// holds too: a request that named a dearer tier but was served a cheaper one pays the
// cheaper one's multiple.
func TestFastBillsTheTierThatAnswered(t *testing.T) {
	for _, named := range []string{"enso-auto", "enso-ultra"} {
		_, _, e := billed(t, kindPremium, true, func(c *ApiController) { Kinds(c.Ctx, KindsFast, true, named) })
		if !e.Cash || !strings.HasPrefix(e.USD, "9.90") {
			t.Errorf("named %s, answered by enso-pro: cash %v usd %q, want enso-pro's 3x, 9.9", named, e.Cash, e.USD)
		}
	}
}

// Fast mode's multiple is the owner's: 2x the router and Flash, 3x Pro and Max, 4x
// Ultra; nothing else has one.
func TestTheFastMultiples(t *testing.T) {
	for id, want := range map[string]int64{"enso-auto": 2, "ENSO": 2, "hanzo/enso": 2, "enso-flash": 2, "enso-pro": 3, "enso-max": 3, "enso-ultra": 4, "zen6": 0, "gpt-6-sol": 0} {
		if got := FastMultiple(id); got != want {
			t.Errorf("FastMultiple(%q) = %d, want %d", id, got, want)
		}
	}
}

// enso-max's quote: Claude Opus 5.5 on Anthropic's key at 1.075.
func TestEnsoMaxQuote(t *testing.T) {
	got := ensoPrices["enso-max"].price()
	want := dollars("4.30", "21.50", "0.215", "5.375")
	for _, c := range [][2]decimal.Decimal{{got.in, want.in}, {got.out, want.out}, {got.read, want.read}, {got.write, want.write}} {
		if c[0].Cmp(c[1]) != 0 {
			t.Errorf("enso-max = %+v, want %+v", got, want)
		}
	}
}

func sum(ns []int64) (s int64) {
	for _, n := range ns {
		s += n
	}
	return s
}
