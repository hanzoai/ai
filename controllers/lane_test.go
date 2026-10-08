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
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// party is who a request comes from, as the balance gate leaves them on it: the
// grant the host's usage policy answered with (nil when it said nothing), and whether
// the platform's paid-lane day is spent when they ask.
type party struct {
	name  string
	grant *object.LimitGrant
	spent bool
	paid  bool // the lane they are owed
}

// planGrant is a grant the host's usage policy answers with.
func planGrant(plan, pays, class string) *object.LimitGrant {
	return &object.LimitGrant{Plan: plan, Pays: pays, Class: class, State: "ok", Settle: func(int64) {}}
}

// holders are the callers every site is asked about: a Max 20x subscriber whose plan
// pays, a wallet with no plan, a guest the policy said nothing about, and the same
// subscriber on a day the platform's paid lane is spent.
func parties() []party {
	return []party{
		{name: "subscriber", grant: planGrant("max-20x", object.PaysPlan, object.ClassPremium), paid: true},
		{name: "wallet without a plan", grant: planGrant("", object.PaysPrepaid, object.ClassPremium)},
		{name: "guest"},
		{name: "subscriber past the platform's day", grant: planGrant("max-20x", object.PaysPlan, object.ClassPremium), spent: true},
	}
}

// lanes makes the lanes real for one test: the platform's switch on, each request on
// the lane its caller earns, and a day with nothing spent on it.
func lanes(t *testing.T) {
	t.Helper()
	day := paidDay
	paidDay = &laneDay{}
	onPaidLane = paidLane
	t.Cleanup(func() {
		paidDay = day
		onPaidLane = func(context.Context) bool { return true }
	})
}

// seat puts c's caller on their lane as the filter chain does: the gate leaves its
// grant, then the lane is decided from it.
func seat(t *testing.T, c *ApiController, h party) *ApiController {
	t.Helper()
	if h.spent {
		t.Setenv("PAID_LANE_DAILY", "1")
		paidDay.add(utcDay(time.Now()), 1_000_000_000)
	}
	if h.grant != nil {
		Cover(c.Ctx, h.grant)
	}
	Lane(c.Ctx)
	return c
}

// onLane checks the response names the lane h is owed, and why a payer is off it.
func onLane(t *testing.T, c *ApiController, h party) {
	t.Helper()
	want := "free"
	if h.paid {
		want = "paid"
	}
	if got := header(c, LaneHeader); got != want {
		t.Errorf("%s = %q, want %q", LaneHeader, got, want)
	}
	reason := ""
	if h.spent {
		reason = ReasonCeiling
	}
	if got := header(c, LaneReasonHeader); got != reason {
		t.Errorf("%s = %q, want %q", LaneReasonHeader, got, reason)
	}
}

// THE LANE IS THE PLAN THAT PAYS. A paid plan's included usage puts a priced call on
// the paid lane, and so does the payer's own credit past it; nothing else does — no
// plan, the Free plan, a model's free daily cap, a free model, and a policy that said
// nothing. With the platform's switch off nobody is on it.
func TestTheLaneIsThePlanThatPays(t *testing.T) {
	cases := []struct {
		name  string
		grant *object.LimitGrant
		paid  bool
	}{
		{"the policy said nothing: a guest, a key nobody can name", nil, false},
		{"a free signup on the Free plan's allowance", planGrant("", object.PaysFree, object.ClassOurs), false},
		{"the Free plan by name", planGrant("free", object.PaysFree, object.ClassOurs), false},
		{"a wallet with no plan", planGrant("", object.PaysPrepaid, object.ClassPremium), false},
		{"granted credit with no plan", planGrant("", object.PaysCredits, object.ClassPremium), false},
		{"Pro's included usage", planGrant("dev", object.PaysPlan, object.ClassPremium), true},
		{"Max 5x's included usage", planGrant("max-5x", object.PaysPlan, object.ClassOurs), true},
		{"Max 20x's included usage", planGrant("max-20x", object.PaysPlan, object.ClassPremium), true},
		{"a Team seat on the org's pool", planGrant("team", object.PaysPlan, object.ClassPremium), true},
		{"Max 20x past its included usage, its prepaid credit", planGrant("max-20x", object.PaysPrepaid, object.ClassPremium), true},
		{"Max 20x past its included usage, its granted credit", planGrant("max-20x", object.PaysCredits, object.ClassPremium), true},
		{"Max 20x on a model's free daily cap", planGrant("max-20x", object.PaysFree, object.ClassPremium), false},
		{"Max 20x in limited mode, on the free model", planGrant("max-20x", object.PaysPlan, object.ClassFree), false},
	}
	for _, on := range []bool{true, false} {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				lanes(t)
				if !on {
					FreeOnly = func() bool { return true }
					t.Cleanup(func() { FreeOnly = func() bool { return false } })
				}
				h := party{grant: tc.grant, paid: tc.paid && on}
				c := seat(t, visit(http.MethodPost, "/v1/chat/completions"), h)
				if got := !FreeOnlyFor(c.Context()); got != h.paid {
					t.Fatalf("switch on %v: on the paid lane = %v, want %v", on, got, h.paid)
				}
				onLane(t, c, h)
			})
		}
	}
}

// A request that never passed the filter that decides — a ZAP twin, a sibling's
// completion over the plane — is on the free lane, whatever its caller holds.
func TestARequestNobodyDecidedIsOnTheFreeLane(t *testing.T) {
	lanes(t)
	c := visit(http.MethodPost, "/v1/chat/completions")
	Cover(c.Ctx, planGrant("max-20x", object.PaysPlan, object.ClassPremium))
	if !FreeOnlyFor(c.Context()) || !FreeOnlyFor(context.Background()) || !FreeOnlyFor(nil) {
		t.Fatal("a request no lane was decided for reached the paid lane")
	}
}

// THE PLATFORM'S DAY IS THE SECOND GUARD. Past PAID_LANE_DAILY a subscriber is on the
// free lane, told why; the next UTC day the lane is theirs again. 0 or a value that is
// not a positive amount closes the lane; unset it is $10.
func TestThePlatformsDayMovesSubscribersToTheFreeLane(t *testing.T) {
	lanes(t)
	t.Setenv("PAID_LANE_DAILY", "1")
	sub := party{grant: planGrant("max-20x", object.PaysPlan, object.ClassPremium), paid: true}
	today := utcDay(time.Now())

	paidDay.add(today, 999_999_999)
	if c := seat(t, visit(http.MethodPost, "/v1/chat/completions"), sub); FreeOnlyFor(c.Context()) {
		t.Fatal("a subscriber under the day's ceiling was put on the free lane")
	}
	paidDay.add(today, 1)
	c := seat(t, visit(http.MethodPost, "/v1/chat/completions"), sub)
	if !FreeOnlyFor(c.Context()) {
		t.Fatal("a subscriber past the day's ceiling stayed on the paid lane")
	}
	onLane(t, c, party{spent: true})
	// A caller who was never owed the paid lane is not told it was spent.
	free := seat(t, visit(http.MethodPost, "/v1/chat/completions"), party{grant: planGrant("", object.PaysPrepaid, object.ClassPremium)})
	onLane(t, free, party{})

	if !paidDay.left(utcDay(time.Now().Add(24*time.Hour)), paidLaneDaily()) {
		t.Fatal("tomorrow's paid lane is spent by today's calls")
	}

	for value, want := range map[string]int64{"": paidLaneDefault, "0": 0, "-3": 0, "ten": 0, "2.5": 2_500_000_000} {
		t.Setenv("PAID_LANE_DAILY", value)
		if got := paidLaneDaily(); got != want {
			t.Errorf("PAID_LANE_DAILY=%q reads %d nano, want %d", value, got, want)
		}
	}
	t.Setenv("PAID_LANE_DAILY", "0")
	if c := seat(t, visit(http.MethodPost, "/v1/chat/completions"), sub); !FreeOnlyFor(c.Context()) {
		t.Fatal("PAID_LANE_DAILY=0 left the paid lane open")
	}
}

// A PAID-LANE CALL IS BILLED, ATTRIBUTED AND COUNTED. Two members of a Team org draw
// on the org's one pool — one subject, the plan's — and each call names the member
// who made it; a call past the plan is the wallet's. Every one carries its price, and
// the platform's day counts the most of its price and its cost. A call on the free
// lane counts nothing against it.
func TestAPaidLaneCallIsBilledAttributedAndCounted(t *testing.T) {
	lanes(t)
	var mu sync.Mutex
	var events []object.UsageEvent
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, u)
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	var settled []int64
	team := planGrant("team", object.PaysPlan, object.ClassPremium)
	team.Settle = func(n int64) { mu.Lock(); settled = append(settled, n); mu.Unlock() }
	past := planGrant("team", object.PaysPrepaid, object.ClassPremium)

	call := func(grant *object.LimitGrant, member string, paid bool) {
		t.Helper()
		ctx := context.WithValue(context.WithValue(context.Background(), planKey{}, grant), laneKey{}, paid)
		billed, cost := int64(3_000_000), int64(4_000_000)
		rec := &usageRecord{Owner: "acme", Model: "anthropic/claude-x", Provider: "relay-a", Status: "success",
			PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, RequestID: member,
			BilledNanoExact: &billed, CostNanoExact: &cost}
		rec.bind(ctx, &iam.User{Owner: "acme", Name: member})
		if err := recordUsage(rec); err != nil {
			t.Fatal(err)
		}
	}
	call(team, "ann", true)
	call(team, "bob", true)
	call(past, "ann", true)
	if got := paidDay.spent; got != 12_000_000 {
		t.Fatalf("the day counted %d nano, want three calls at their 4,000,000 nano cost", got)
	}
	call(team, "cat", false)
	if got := paidDay.spent; got != 12_000_000 {
		t.Fatalf("a free-lane call counted against the paid lane's day: %d", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 4 {
		t.Fatalf("%d usage events, want 4: %+v", len(events), events)
	}
	for i, want := range []struct {
		actor string
		plan  bool
		by    string
	}{{"acme/ann", true, object.PaysPlan}, {"acme/bob", true, object.PaysPlan}, {"acme/ann", false, ""}, {"acme/cat", true, object.PaysPlan}} {
		ev := events[i]
		if ev.Subject != "acme" || ev.Namespace != "acme" {
			t.Errorf("event %d draws on %s in %s, want the org's one pool", i, ev.Subject, ev.Namespace)
		}
		if ev.Actor != want.actor || ev.Plan != want.plan || ev.PaidBy != want.by {
			t.Errorf("event %d: actor %q plan %v paid-by %q, want %q %v %q", i, ev.Actor, ev.Plan, ev.PaidBy, want.actor, want.plan, want.by)
		}
		if ev.USD != "0.003" {
			t.Errorf("event %d billed %q, want its price 0.003", i, ev.USD)
		}
	}
	if len(settled) != 3 || settled[0] != 3_000_000 || settled[1] != 3_000_000 {
		t.Errorf("the plan settled %v, want each covered call at its price", settled)
	}
}

// ── every site, asked about every caller ─────────────────────────────────────

// chatAs sends body to /v1/chat/completions as h, strict when asked.
func (w *relayWorld) chatAs(t *testing.T, h party, body string, strict bool) *ApiController {
	t.Helper()
	c := seat(t, as(visit(http.MethodPost, "/v1/chat/completions"), w.cred), h)
	if strict {
		c.Fiber().Request().Header.Set(strictHeader, "1")
	}
	c.Fiber().Request().SetBody([]byte(body))
	c.ChatCompletions()
	return c
}

// The relay — a third-party route's own vendors (candidates, forward), its tool and
// image requests, and a strict request taking the route's first row — serves the paid
// lane to a subscriber and to nobody else: the vendor is never asked for anyone else,
// and the subscriber's answer is billed.
func TestTheRelayServesThePaidLaneToSubscribersOnly(t *testing.T) {
	for _, site := range []struct {
		name, body string
		strict     bool
	}{
		{"text", `{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`, false},
		{"tools", `{"model":"relay-sku","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`, false},
		{"strict", `{"model":"relay-sku","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, true},
	} {
		for _, h := range parties() {
			t.Run(site.name+"/"+h.name, func(t *testing.T) {
				lanes(t)
				w := newRelayWorld(t, completes("ok", 10, 0, 5), never(t))
				c := w.chatAs(t, h, site.body, site.strict)
				onLane(t, c, h)
				if !h.paid {
					if answered(c) != http.StatusServiceUnavailable || !strings.Contains(sent(c), "third-party models are not being served") {
						t.Fatalf("status %d, want the free lane's plain 503: %s", answered(c), sent(c))
					}
					if w.a.asked() != 0 {
						t.Fatalf("the paid vendor was asked %d time(s) for a caller on the free lane", w.a.asked())
					}
					return
				}
				if answered(c) != http.StatusOK || w.a.asked() != 1 {
					t.Fatalf("status %d, vendor asked %d: want the subscriber served by the route's vendor: %s", answered(c), w.a.asked(), sent(c))
				}
				if ev := w.paid(t); !ev.Plan || ev.USD == "" || ev.USD == "0" {
					t.Fatalf("the subscriber's answer was filed %+v, want it billed to the plan", ev)
				}
			})
		}
	}
}

// pipeAs drives the family relay for sku as h and returns what the client got.
func pipeAs(t *testing.T, fam *modelFamily, sku string, h party) (*ApiController, []attempt) {
	t.Helper()
	body := []byte(`{"model":"` + sku + `","messages":[{"role":"user","content":"2+2?"}]}`)
	c := seat(t, visit(http.MethodPost, "/v1/chat/completions"), h)
	c.Fiber().Request().SetBody(body)
	out := c.pipeToFamily(fam, "chat/completions", "openai", sku, body, false, 0, "acme", &iam.User{Owner: "acme", Name: "ann"}, false, nil, time.Now())
	return c, out
}

// A priced third-party route a family carries is sent for a subscriber and for nobody
// else, and nothing answers in its place for anyone else.
func TestAFamilysPricedThirdPartyRouteIsSentForSubscribersOnly(t *testing.T) {
	const free, paid = "vendor/big:free", "vendor/paid-a"
	for _, h := range parties() {
		t.Run(h.name, func(t *testing.T) {
			lanes(t)
			cooled.forget()
			forgetKeys()
			t.Setenv("OPENROUTER_API_KEY", "k1")
			t.Setenv("OPENROUTER_API_KEY_2", "")
			t.Setenv("OPENROUTER_API_KEY_3", "")
			fake := &refuses{status: http.StatusOK, free: free,
				body: `{"id":"gen-2","model":"` + paid + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`}
			vendor := fake.serve(t)
			defer vendor.Close()
			fam := spareFamily(t, vendor.URL, free, paid)

			c, out := pipeAs(t, fam, paid, h)
			onLane(t, c, h)
			if !h.paid {
				if out != nil || len(fake.asked) != 0 || !strings.Contains(sent(c), "third-party models are not being served") {
					t.Fatalf("attempts %+v, asked %v, body %s: want nothing sent and the plain reason", out, fake.asked, sent(c))
				}
				return
			}
			if out != nil || len(fake.asked) != 1 || fake.asked[0] != paid {
				t.Fatalf("attempts %+v, asked %v: want the priced route sent once", out, fake.asked)
			}
		})
	}
}

// A priced Hanzo SKU is sent to its own paid route for a subscriber; for anyone else —
// the subscriber past the platform's day included — the free routes that stand in for
// it answer, as an answer and not an error.
func TestAPricedHanzoSKUFallsToTheFreeLaneAsAnAnswer(t *testing.T) {
	const free = "vendor/big:free"
	for _, h := range parties() {
		t.Run(h.name, func(t *testing.T) {
			lanes(t)
			cooled.forget()
			forgetKeys()
			t.Setenv("OPENROUTER_API_KEY", "k1")
			t.Setenv("OPENROUTER_API_KEY_2", "")
			t.Setenv("OPENROUTER_API_KEY_3", "")
			prev := object.UsageRecorder()
			object.SetUsageRecorder(func(context.Context, object.UsageEvent) error { return nil })
			t.Cleanup(func() { object.SetUsageRecorder(prev) })

			pool := &refuses{status: http.StatusServiceUnavailable, body: `{}`, free: free}
			srv := pool.serve(t)
			defer srv.Close()
			spareFamily(t, srv.URL, free)
			_, spends := planEnso(t, false, "0")
			ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro", Plan: true}}
			m := ensoFam.byID["enso-pro"]
			m.Base.In, m.Base.Out = decimal.New(3, 0), decimal.New(15, 0)
			ensoFam.byID["enso-pro"] = m

			c, out := pipeAs(t, ensoFam, "enso-pro", h)
			if out != nil {
				t.Fatalf("attempts %+v, want an answer", out)
			}
			drain(t, c)
			onLane(t, c, h)
			if answered(c) != http.StatusOK {
				t.Fatalf("status %d, want an answer on either lane: %s", answered(c), sent(c))
			}
			if h.paid {
				if len(spends()) != 1 || len(pool.asked) != 0 {
					t.Fatalf("enso asked %d, pool asked %v: want the SKU's own route", len(spends()), pool.asked)
				}
				return
			}
			if len(spends()) != 0 || len(pool.asked) == 0 || pool.asked[0] != free {
				t.Fatalf("enso asked %d, pool asked %v: want only a free route", len(spends()), pool.asked)
			}
		})
	}
}

// Jev is bought per call: a subscriber's decision asks it, and nobody else's does.
func TestJevIsAskedForSubscribersOnly(t *testing.T) {
	for _, h := range parties() {
		t.Run(h.name, func(t *testing.T) {
			lanes(t)
			fake, _ := setupDecisions(t)
			fake.answer = jevAnswer
			c := seat(t, presenting(visit(http.MethodPost, decisionsPath), "Bearer "+decisionsKey), h)
			c.Fiber().Request().SetBody([]byte(strings.Replace(decisionBody, `"model":"kai"`, `"model":"typesafe/jev-1.13"`, 1)))
			status := answering(t, c, c.Decisions)
			settled(t)
			calls, _, _ := fake.seen()
			if h.paid {
				if status != http.StatusOK || calls != 1 {
					t.Fatalf("status %d, Jev asked %d: want the subscriber's decision answered (%s)", status, calls, sent(c))
				}
				return
			}
			if status == http.StatusOK || calls != 0 {
				t.Fatalf("status %d, Jev asked %d: want no Jev for a caller on the free lane (%s)", status, calls, sent(c))
			}
		})
	}
}

// A vendor's embeddings, speech, images and video spend for a subscriber and are
// refused for everyone else; a family's own service and ours spend nothing either way.
func TestAVendorsMediaSpendsForSubscribersOnly(t *testing.T) {
	vendor := &object.Provider{Owner: "admin", Name: "openai-direct", Type: "OpenAI"}
	ours := []*object.Provider{{Type: "Zen"}, {Type: "Enso"}, {Owner: "admin", Name: "speech", Type: "OpenAI"}}
	for _, h := range parties() {
		t.Run(h.name, func(t *testing.T) {
			lanes(t)
			c := seat(t, visit(http.MethodPost, "/v1/images/generations"), h)
			if got := paying(c.Context(), vendor); got == h.paid {
				t.Fatalf("refused = %v for a caller owed the paid lane %v", got, h.paid)
			}
			for _, p := range ours {
				if paying(c.Context(), p) {
					t.Fatalf("%s/%s was refused: it spends nothing", p.Type, p.Name)
				}
			}
		})
	}
}
