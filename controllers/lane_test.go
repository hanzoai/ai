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
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
	"github.com/luxfi/zap"
)

// usd is n US dollars in nano-dollars.
func usd(n float64) int64 { return int64(n * 1e9) }

// party is who a request comes from, as the balance gate leaves them on it: the
// grant the host's usage policy answered with (nil when it said nothing), and whether
// the platform's paid-lane day is spent when they ask.
type party struct {
	name  string
	grant *object.LimitGrant
	spent bool
	paid  bool // the lane they are owed
}

// planGrant is a grant the host's usage policy answers with; a plan that pays has $5
// of its class left.
func planGrant(plan, pays, class string) *object.LimitGrant {
	return &object.LimitGrant{Plan: plan, Pays: pays, Class: class, State: "ok", Spend: usd(5), Settle: func(int64) {}}
}

// parties are the callers every site is asked about: a Max 20x subscriber whose plan
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

// lanes makes the lanes real for one test: the platform's switch on and a day of its
// own, kept in no store, with nothing spent on it.
func lanes(t *testing.T) {
	t.Helper()
	day := paidDay
	paidDay = &laneBook{}
	t.Cleanup(func() { paidDay = day })
}

// spendDay counts n nano-dollars of org's spend against today's paid-lane day.
func spendDay(org string, n int64) {
	paidDay.settle(&seat{book: paidDay, org: org}, n, time.Now())
}

// seatAs puts c's caller on their lane as the gate does: it leaves its grant, and the
// lane is decided from it, once.
func seatAs(t *testing.T, c *ApiController, h party) *ApiController {
	t.Helper()
	if h.spent {
		t.Setenv("PAID_LANE_DAILY", "1")
		spendDay("other", usd(1))
	}
	if h.grant != nil {
		Cover(c.Ctx, h.grant)
	}
	Seat(c.Ctx, h.grant, "acme", "acme", "")
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
// nothing. With the platform's switch off nobody is on it, and the response says
// nothing about lanes.
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
			t.Run(fmt.Sprintf("switch on %v/%s", on, tc.name), func(t *testing.T) {
				lanes(t)
				if !on {
					FreeOnly = func() bool { return true }
					t.Cleanup(func() { FreeOnly = func() bool { return false } })
				}
				h := party{grant: tc.grant, paid: tc.paid && on}
				c := seatAs(t, visit(http.MethodPost, "/v1/chat/completions"), h)
				if got := !FreeOnlyFor(c.Context()); got != h.paid {
					t.Fatalf("on the paid lane = %v, want %v", got, h.paid)
				}
				if !on {
					if header(c, LaneHeader) != "" || header(c, LaneReasonHeader) != "" {
						t.Fatalf("the switch is off and the response names a lane: %q %q", header(c, LaneHeader), header(c, LaneReasonHeader))
					}
					return
				}
				onLane(t, c, h)
			})
		}
	}
}

// A request that never passed the gate — a ZAP handler's — is on the free lane,
// whatever its caller holds.
func TestARequestNobodySeatedIsOnTheFreeLane(t *testing.T) {
	lanes(t)
	covered := context.WithValue(context.Background(), planKey{}, planGrant("max-20x", object.PaysPlan, object.ClassPremium))
	if !FreeOnlyFor(covered) || !FreeOnlyFor(context.Background()) || !FreeOnlyFor(nil) {
		t.Fatal("a request no lane was decided for reached the paid lane")
	}
}

// THE PLATFORM'S DAY IS THE SECOND GUARD. A subscriber is seated only while the day has
// room for the call's estimate; past it they are on the free lane, told why; the next
// UTC day the lane is theirs again. 0, or a value that is not a finite amount between
// 0 and $1,000,000,000, closes the lane; unset it is $10.
func TestThePlatformsDayMovesSubscribersToTheFreeLane(t *testing.T) {
	lanes(t)
	t.Setenv("PAID_LANE_DAILY", "1")
	t.Setenv("PAID_LANE_ORG_SHARE", "1")
	sub := party{grant: planGrant("max-20x", object.PaysPlan, object.ClassPremium), paid: true}
	now := time.Now()

	spendDay("other", usd(0.5))
	if c := seatAs(t, visit(http.MethodPost, "/v1/chat/completions"), sub); FreeOnlyFor(c.Context()) {
		t.Fatal("a subscriber whose call fits the day was put on the free lane")
	}
	spendDay("other", usd(0.5))
	c := seatAs(t, visit(http.MethodPost, "/v1/chat/completions"), sub)
	if !FreeOnlyFor(c.Context()) {
		t.Fatal("a subscriber past the day's ceiling stayed on the paid lane")
	}
	onLane(t, c, party{spent: true})
	if err := laneOff(c.Context(), "vendor/x"); statusOf(err) != http.StatusTooManyRequests || codeOf(err) != codeLaneFull {
		t.Fatalf("a call only the paid lane serves is refused %d %q, want 429 %s", statusOf(err), codeOf(err), codeLaneFull)
	}
	// A caller who was never owed the paid lane is not told it was full.
	free := seatAs(t, visit(http.MethodPost, "/v1/chat/completions"), party{grant: planGrant("", object.PaysPrepaid, object.ClassPremium)})
	onLane(t, free, party{})
	if err := laneOff(free.Context(), "vendor/x"); statusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("a caller never owed the lane is refused %d, want the switch's 503", statusOf(err))
	}

	if s := paidDay.reserve("acme", "", 1, math.MaxInt64, now.Add(24*time.Hour)); s == nil {
		t.Fatal("tomorrow's paid lane is spent by today's calls")
	}

	for value, want := range map[string]int64{
		"": paidLaneDefault, "0": 0, "-3": 0, "ten": 0, "2.5": usd(2.5), "1e9": usd(1e9),
		"Inf": 0, "+Inf": 0, "NaN": 0, "1e10": 0, "9223372037": 0,
	} {
		t.Setenv("PAID_LANE_DAILY", value)
		if got := paidLaneDaily(); got != want {
			t.Errorf("PAID_LANE_DAILY=%q reads %d nano, want %d", value, got, want)
		}
	}
	for value, want := range map[string]int64{"": usd(2.5), "0.5": usd(5), "1": usd(10), "0": 0, "1.5": 0, "half": 0, "NaN": 0} {
		t.Setenv("PAID_LANE_ORG_SHARE", value)
		if got := orgShare(usd(10)); got != want {
			t.Errorf("PAID_LANE_ORG_SHARE=%q gives an org %d nano of $10, want %d", value, got, want)
		}
	}
	t.Setenv("PAID_LANE_ORG_SHARE", "1")
	t.Setenv("PAID_LANE_DAILY", "0")
	if c := seatAs(t, visit(http.MethodPost, "/v1/chat/completions"), sub); !FreeOnlyFor(c.Context()) {
		t.Fatal("PAID_LANE_DAILY=0 left the paid lane open")
	}
}

// ONE ORG HOLDS AT MOST ITS SHARE. With the platform's day at $10 and the default
// share, one org's calls hold and spend at most $2.50 of it; another org is seated
// beside it.
func TestOneOrgHoldsAtMostItsShareOfTheDay(t *testing.T) {
	lanes(t)
	t.Setenv("PAID_LANE_DAILY", "10")
	now := time.Now()
	var held []*seat
	for paidDay.reserve("acme", "", usd(1), math.MaxInt64, now) != nil {
		held = append(held, &seat{})
		if len(held) > 10 {
			t.Fatal("one org was seated past its share")
		}
	}
	if len(held) != 2 {
		t.Fatalf("acme was seated for $%d of $1 calls, want 2 within its $2.50", len(held))
	}
	if paidDay.reserve("globex", "", usd(1), math.MaxInt64, now) == nil {
		t.Fatal("another org was refused while the day had room for it")
	}
}

// NEITHER THE DAY NOR THE PLAN IS PASSED BY CALLS IN FLIGHT. Fifty concurrent
// 200,000-token prompts at a premium model, from a payer whose plan has $0.01 of its
// class left: none fits, so none is seated and nothing is held. Then many concurrent
// calls from payers whose plans have room, under a $10 day: every one seated held its
// estimate before it was served, and what was seated and what was spent never pass
// the day, an org's share, or a plan's figure — each call spending what it estimated.
func TestCallsInFlightNeverPassTheDayOrThePlan(t *testing.T) {
	lanes(t)
	t.Setenv("PAID_LANE_DAILY", "10")
	t.Setenv("PAID_LANE_ORG_SHARE", "1")
	premium := strings.Repeat("x", 600_000) // about 200,000 tokens
	body := []byte(`{"model":"anthropic/claude-opus-5.5","max_tokens":32000,"messages":[{"role":"user","content":"` + premium + `"}]}`)
	low := planGrant("max-20x", object.PaysPlan, object.ClassPremium)
	low.Spend = usd(0.01)

	var wg sync.WaitGroup
	var mu sync.Mutex
	seated := 0
	for range 50 {
		wg.Go(func() {
			c := visit(http.MethodPost, "/v1/chat/completions")
			c.Fiber().Request().SetBody(body)
			Cover(c.Ctx, low)
			if Seat(c.Ctx, low, "acme", "acme", "anthropic/claude-opus-5.5") == SeatPaid {
				mu.Lock()
				seated++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if seated != 0 || paidDay.held != 0 {
		t.Fatalf("%d calls seated holding %d nano for a payer with $0.01 left, want none", seated, paidDay.held)
	}

	// Payers with room: four orgs, each plan with $3 left, $0.40 calls.
	const est = 400_000_000
	plan := func(org string) *object.LimitGrant {
		g := planGrant("max-20x", object.PaysPlan, object.ClassPremium)
		g.Spend = usd(3)
		return g
	}
	now := time.Now()
	var seats []*seat
	var peak int64
	orgs := []string{"a", "b", "c", "d"}
	for range 100 {
		for _, org := range orgs {
			wg.Go(func() {
				g := plan(org)
				s := paidDay.reserve(org, org+"\x00"+org+"\x00"+g.Class, est, g.Spend, now)
				if s == nil {
					return
				}
				paidDay.mu.Lock()
				now := paidDay.held + paidDay.spent
				paidDay.mu.Unlock()
				mu.Lock()
				seats = append(seats, s)
				peak = max(peak, now)
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	if len(seats) != 25 {
		t.Fatalf("%d $0.40 calls seated under a $10 day, want 25", len(seats))
	}
	if paidDay.held > usd(10) || peak > usd(10) {
		t.Fatalf("calls in flight hold %d nano (peak %d), past the $10 day", paidDay.held, peak)
	}
	per := map[string]int64{}
	for _, s := range seats {
		per[s.org] += s.held
	}
	for org, n := range per {
		if n > usd(3) {
			t.Fatalf("%s's calls in flight hold %d nano, past the $3 its plan has left", org, n)
		}
	}
	for _, s := range seats {
		wg.Go(func() { s.settle(est) })
	}
	wg.Wait()
	if paidDay.spent > usd(10) || paidDay.held != 0 {
		t.Fatalf("the day spent %d nano with %d still held, want at most $10 and nothing held", paidDay.spent, paidDay.held)
	}
	if paidDay.reserve("e", "", est, math.MaxInt64, now) != nil {
		t.Fatal("a call was seated on a spent day")
	}
}

// A seat nothing settles gives its hold back: when its handler is done, or after
// seatLapse for one a job or a stream kept. A kept seat outlives its handler.
func TestASeatNothingSettlesGivesItsHoldBack(t *testing.T) {
	lanes(t)
	now := time.Now()
	s := paidDay.reserve("acme", "", usd(1), math.MaxInt64, now)
	s.end()
	if paidDay.held != 0 {
		t.Fatalf("an ended seat still holds %d nano", paidDay.held)
	}
	k := paidDay.reserve("acme", "", usd(1), math.MaxInt64, now)
	k.keep()
	k.end()
	if paidDay.held != usd(1) {
		t.Fatalf("a kept seat holds %d nano after its handler, want its $1", paidDay.held)
	}
	paidDay.mu.Lock()
	paidDay.lapse(now.Add(seatLapse + time.Second))
	paidDay.mu.Unlock()
	if paidDay.held != 0 {
		t.Fatalf("a seat past its lapse still holds %d nano", paidDay.held)
	}
}

// THE DAY OUTLIVES THE PROCESS. What was spent is kept in the store as it settles, and
// a new process reads it back the first time it is asked about the day.
func TestThePaidLaneDaySurvivesARestart(t *testing.T) {
	withStore(t)
	t.Setenv("PAID_LANE_DAILY", "1")
	t.Setenv("PAID_LANE_ORG_SHARE", "1")
	now := time.Now()
	first := &laneBook{store: true}
	s := first.reserve("acme", "", usd(0.5), math.MaxInt64, now)
	if s == nil {
		t.Fatal("an empty day refused a call")
	}
	first.settle(s, usd(0.75), now)

	restarted := &laneBook{store: true}
	if restarted.reserve("acme", "", usd(0.5), math.MaxInt64, now) != nil {
		t.Fatal("after a restart the day forgot $0.75 of spend and seated a $0.50 call under $1")
	}
	if restarted.spent != usd(0.75) || restarted.use("acme").spent != usd(0.75) {
		t.Fatalf("after a restart the day reads %d nano, acme %d, want $0.75", restarted.spent, restarted.use("acme").spent)
	}
	if restarted.reserve("acme", "", usd(0.25), math.MaxInt64, now) == nil {
		t.Fatal("after a restart a call that fits what is left was refused")
	}
}

// A PAID-LANE CALL IS BILLED, ATTRIBUTED AND COUNTED. Two members of a Team org draw
// on the org's one pool — one subject, the plan's — and each call names the member
// who made it; a call past the plan is the wallet's. Every one carries its price, and
// its seat counts the most of its price and its cost against the day. A call on the
// free lane counts nothing, and neither does one on the org's own key.
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

	call := func(grant *object.LimitGrant, member string, paid, byo bool) {
		t.Helper()
		l := &laneState{}
		if paid {
			l.seat = paidDay.reserve("acme", "", usd(0.01), math.MaxInt64, time.Now())
		}
		ctx := context.WithValue(context.WithValue(context.Background(), planKey{}, grant), laneKey{}, l)
		billed, cost := int64(3_000_000), int64(4_000_000)
		rec := &usageRecord{Owner: "acme", Model: "anthropic/claude-x", Provider: "relay-a", Status: "success",
			PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, RequestID: member,
			BilledNanoExact: &billed, CostNanoExact: &cost, BYO: byo}
		rec.bind(ctx, &iam.User{Owner: "acme", Name: member})
		if err := recordUsage(rec); err != nil {
			t.Fatal(err)
		}
	}
	call(team, "ann", true, false)
	call(team, "bob", true, false)
	call(past, "ann", true, false)
	if paidDay.spent != 12_000_000 || paidDay.held != 0 {
		t.Fatalf("the day counted %d nano with %d held, want three calls at their 4,000,000 nano cost and nothing held", paidDay.spent, paidDay.held)
	}
	call(team, "cat", false, false)
	call(team, "dan", true, true)
	if paidDay.spent != 12_000_000 || paidDay.held != 0 {
		t.Fatalf("a free-lane call or the org's own key counted against the paid lane's day: %d, %d held", paidDay.spent, paidDay.held)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 5 {
		t.Fatalf("%d usage events, want 5: %+v", len(events), events)
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
	if len(settled) < 3 || settled[0] != 3_000_000 || settled[1] != 3_000_000 {
		t.Errorf("the plan settled %v, want each covered call at its price", settled)
	}
}

// A FAST REQUEST'S LOSERS AND A VIDEO JOB COUNT ON THEIR REQUEST'S SEAT. A provider
// beaten in a race is billed after its request is over, and a video is billed when a
// later poll sees it done: both settle the seat their request was admitted on, never
// a poll's or a background's.
func TestRaceLosersAndVideoJobsCountOnTheirRequestsSeat(t *testing.T) {
	lanes(t)
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(context.Context, object.UsageEvent) error { return nil })
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().Header.Set("X-Fast", "1")
	Seat(c.Ctx, planGrant("max-20x", object.PaysPrepaid, object.ClassPremium), "acme", "acme", "anthropic/claude-x")
	if FreeOnlyFor(c.Context()) {
		t.Fatal("the subscriber was not seated")
	}
	bill := c.billRaced("anthropic/claude-x", &iam.User{Owner: "acme", Name: "ann"}, true, false, "r1", time.Now())
	bill(attempt{provider: "relay-b", prompt: 1000, completion: 100, err: fmt.Errorf("beaten")})
	if paidDay.spent <= 0 || paidDay.held != 0 {
		t.Fatalf("the race's loser counted %d nano with %d held, want its cost counted on the request's seat", paidDay.spent, paidDay.held)
	}

	before := paidDay.spent
	v := visit(http.MethodPost, "/v1/videos/generations")
	Seat(v.Ctx, planGrant("max-20x", object.PaysPrepaid, object.ClassPremium), "acme", "acme", "wan2-2-t2v-a14b")
	job := &videoJob{id: "video_lane", userModel: "wan2-2-t2v-a14b", hold: &budgetHold{}, bill: context.WithoutCancel(v.Context()), seat: seatOf(v.Context()), createdAt: time.Now()}
	job.seat.keep()
	Unseat(v.Ctx)
	if paidDay.held != usd(0.4) {
		t.Fatalf("a video job's seat holds %d nano after its create, want its $0.40", paidDay.held)
	}
	poll := visit(http.MethodGet, "/v1/videos/video_lane")
	if job.markCompleted(videoCostCents(job.userModel, 1)) {
		poll.recordVideoUsage(job.bill, &iam.User{Owner: "acme", Name: "ann"}, &object.Provider{Owner: "admin", Name: "do-ai"}, job.userModel, false, 1, "success", "", time.Now())
	}
	if paidDay.spent-before != usd(0.4) || paidDay.held != 0 {
		t.Fatalf("the finished video counted %d nano with %d held, want its $0.40 on the create's seat", paidDay.spent-before, paidDay.held)
	}
}

// A family is sent the plan's spend only for a request on the paid lane, and never
// more than its seat holds: a subscriber handed to the free lane — the day full, a
// Hanzo SKU served by what stands in for it — buys no paid rung.
func TestAFamilyIsSentSpendOnlyOnThePaidLane(t *testing.T) {
	restore(t, ensoFam)
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro", Plan: true}}
	g := planGrant("max-20x", object.PaysPlan, object.ClassOurs)
	if got := spendOf(g, nil, &planMark{}, ensoFam, "enso-pro"); got != "" {
		t.Fatalf("a request off the paid lane was sent spend %q", got)
	}
	if got := spendOf(g, &seat{held: usd(0.25)}, &planMark{}, ensoFam, "enso-pro"); got != "0.250000000" {
		t.Fatalf("a seated request was sent spend %q, want what its seat holds", got)
	}
	if got := spendOf(g, &seat{held: math.MaxInt64}, &planMark{}, ensoFam, "enso-pro"); got != "5.000000000" {
		t.Fatalf("a seated request was sent spend %q, want what the plan has left", got)
	}
}

// THE ORG'S OWN KEY IS ITS OWN. While the switch is on, a call to a row the org holds
// for itself is served off the paid lane and counts nothing against the day; with the
// switch off it is refused, as every priced call is.
func TestTheOrgsOwnKeyIsNotThePlatformsSpend(t *testing.T) {
	ctx := context.Background()
	mine := &object.Provider{Owner: "acme", Name: "openai", Type: "OpenAI"}
	ours := &object.Provider{Owner: "admin", Name: "openai", Type: "OpenAI"}
	if paying(ctx, mine) || !paying(ctx, ours) {
		t.Fatalf("off the paid lane: own key refused %v, platform's refused %v; want only the platform's", paying(ctx, mine), paying(ctx, ours))
	}
	if shut(paidSeat(ctx), ours) {
		t.Fatal("a seated request was refused the platform's provider")
	}
	if laneSpend(&usageRecord{BYO: true, PromptTokens: 1000, CompletionTokens: 1000, Model: "gpt-4o"}) != 0 {
		t.Fatal("a call on the org's own key counted against the platform's day")
	}
	FreeOnly = func() bool { return true }
	t.Cleanup(func() { FreeOnly = func() bool { return false } })
	if !paying(ctx, mine) {
		t.Fatal("with the switch off the org's own key was served; it is refused as before")
	}
}

// THE ESTIMATE IS THE WORST CASE. A conversation is held at its prompt and its
// completion ceiling, twice for fast mode; an inline image as a picture, not its
// bytes; a video at its clip price.
func TestTheEstimateIsTheWorstCase(t *testing.T) {
	at := func(path, body string, fast bool) int64 {
		c := visit(http.MethodPost, path)
		c.Fiber().Request().SetBody([]byte(body))
		if fast {
			c.Fiber().Request().Header.Set("X-Fast", "1")
		}
		return estimate(c.Ctx, "gpt-4o")
	}
	small := at("/v1/chat/completions", `{"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, false)
	if want := worst("gpt-4o", 1, reserveCompletionTokens(100)); small < want {
		t.Fatalf("a chat is held at %d nano, below its completion ceiling's %d", small, want)
	}
	if at("/v1/chat/completions", `{"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, true) != 2*small {
		t.Fatal("a fast chat is not held for both providers it races")
	}
	big := at("/v1/chat/completions", `{"max_tokens":100,"messages":[{"role":"user","content":"`+strings.Repeat("y", 300_000)+`"}]}`, false)
	if big < worst("gpt-4o", 100_000, 0) {
		t.Fatalf("a 300 KB prompt is held at %d nano, below 100,000 tokens", big)
	}
	pic := at("/v1/chat/completions", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+strings.Repeat("A", 3_000_000)+`"}}]}]}`, false)
	if pic >= worst("gpt-4o", 1_000_000, reserveCompletionTokens(0)) {
		t.Fatalf("an inline image is held as its encoded bytes: %d nano", pic)
	}
	if got := estimate(visit(http.MethodPost, "/v1/videos/generations").Ctx, "wan2-2-t2v-a14b"); got != usd(0.4) {
		t.Fatalf("a video is held at %d nano, want its $0.40 clip price", got)
	}
}

// ── every site, asked about every caller ─────────────────────────────────────

// chatAs sends body to /v1/chat/completions as h, strict when asked.
func (w *relayWorld) chatAs(t *testing.T, h party, body string, strict bool) *ApiController {
	t.Helper()
	c := seatAs(t, as(visit(http.MethodPost, "/v1/chat/completions"), w.cred), h)
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
// and the subscriber's answer is billed. A subscriber the lane had no room for is told
// so, 429, rather than that nothing is served.
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
					want := http.StatusServiceUnavailable
					if h.spent {
						want = http.StatusTooManyRequests
					}
					if answered(c) != want || !strings.Contains(sent(c), "not being served") {
						t.Fatalf("status %d, want the free lane's plain %d: %s", answered(c), want, sent(c))
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
				if paidDay.held != 0 || paidDay.spent <= 0 {
					t.Fatalf("the subscriber's answer left %d nano held and %d counted, want its seat settled at what it cost", paidDay.held, paidDay.spent)
				}
			})
		}
	}
}

// A PAY-AS-YOU-GO CALLER ON ZAP NEVER REACHES A PAID VENDOR, with the platform's
// switch on or off: a ZAP request is not behind the gate, so it holds no seat, and its
// handlers ask the same lane decision as the HTTP handlers do. The same request on a
// seat is sent to the vendor, so the lane is what refused it.
func TestAPayAsYouGoCallerOnZAPNeverReachesAPaidVendor(t *testing.T) {
	chat := []byte(`{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`)
	embed := []byte(`{"model":"relay-sku","input":"hi"}`)
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("switch on %v", on), func(t *testing.T) {
			lanes(t)
			if !on {
				FreeOnly = func() bool { return true }
				t.Cleanup(func() { FreeOnly = func() bool { return false } })
			}
			w := newRelayWorld(t, completes("ok", 10, 0, 5), never(t))
			ctx := context.Background()
			for name, call := range map[string]func() (*zap.Message, error){
				"chat.completions": func() (*zap.Message, error) {
					return handleCloudService(ctx, "", cloudCall(t, "chat.completions", w.cred, string(chat)))
				},
				"/v1/chat/completions": func() (*zap.Message, error) {
					return gateway(nil)(ctx, "", gatewayCall(t, "/v1/chat/completions", map[string]string{"Authorization": w.cred}, string(chat)))
				},
				"embeddings": func() (*zap.Message, error) { return zapEmbeddingsHandler(ctx, w.cred, embed) },
				"responses": func() (*zap.Message, error) {
					return zapResponsesHandler(ctx, w.cred, []byte(`{"model":"relay-sku","input":"hi"}`))
				},
			} {
				status, said := cloudReply(t, call)
				if status < 400 || w.a.asked() != 0 {
					t.Fatalf("%s: status %d (%s), vendor asked %d: a PAYG caller on ZAP reached a paid vendor", name, status, said, w.a.asked())
				}
				if !strings.Contains(said, "not being served") {
					t.Fatalf("%s: refused %d %q, want the lane's refusal", name, status, said)
				}
			}
			if !on {
				return
			}
			status, said := cloudReply(t, func() (*zap.Message, error) { return zapEmbeddingsHandler(paidSeat(ctx), w.cred, embed) })
			if strings.Contains(said, "not being served") || w.a.asked() == 0 {
				t.Fatalf("a seated request: status %d (%s), vendor asked %d, want it sent", status, said, w.a.asked())
			}
		})
	}
}

// cloudReply is a native ZAP reply's status and error text.
func cloudReply(t *testing.T, call func() (*zap.Message, error)) (uint32, string) {
	t.Helper()
	msg, err := call()
	if err != nil || msg == nil {
		t.Fatalf("no reply: %v", err)
	}
	root := msg.Root()
	return root.Uint32(object.CloudRespStatus), root.Text(object.CloudRespError)
}

// pipeAs drives the family relay for sku as h and returns what the client got.
func pipeAs(t *testing.T, fam *modelFamily, sku string, h party) (*ApiController, []attempt) {
	t.Helper()
	body := []byte(`{"model":"` + sku + `","messages":[{"role":"user","content":"2+2?"}]}`)
	c := seatAs(t, visit(http.MethodPost, "/v1/chat/completions"), h)
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
				if out != nil || len(fake.asked) != 0 || !strings.Contains(sent(c), "not being served") {
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
// it answer, as an answer and not an error, and carry no spend.
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
			c := seatAs(t, presenting(visit(http.MethodPost, decisionsPath), "Bearer "+decisionsKey), h)
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
			c := seatAs(t, visit(http.MethodPost, "/v1/images/generations"), h)
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

// THE ORG'S OWN KEY IS SERVED WHEN THE LANE IS FULL. A subscriber whose org connected
// its own key for a route is served by that key with the platform's day spent; the
// platform's vendors on the route are not asked, and nothing is counted against the
// day. The gate knows such a call by its route (OwnKey) and never hands it to the
// free model.
func TestTheOrgsOwnKeyIsServedWhenTheLaneIsFull(t *testing.T) {
	lanes(t)
	w := newRelayWorld(t, never(t), never(t))
	mine := script(t, completes("mine", 10, 0, 5))
	if _, err := object.AddProvider(&object.Provider{
		Owner: "relayco", Name: "relay-a", Category: "Model", Type: "OpenAI",
		ProviderUrl: mine.url, ClientSecret: "k", State: "Active",
	}); err != nil {
		t.Fatal(err)
	}
	object.InvalidateProviderNameCache("")
	if !OwnKey("relayco", relaySku) || OwnKey("globex", relaySku) {
		t.Fatalf("OwnKey relayco %v globex %v: want only the org that holds the row", OwnKey("relayco", relaySku), OwnKey("globex", relaySku))
	}
	c := w.chatAs(t, party{grant: planGrant("max-20x", object.PaysPlan, object.ClassPremium), spent: true}, `{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`, false)
	if answered(c) != http.StatusOK || mine.asked() != 1 || w.a.asked() != 0 || w.b.asked() != 0 {
		t.Fatalf("status %d, own key asked %d, platform asked %d/%d: want the org's own key to answer (%s)", answered(c), mine.asked(), w.a.asked(), w.b.asked(), sent(c))
	}
	if paidDay.spent != usd(1) || paidDay.held != 0 {
		t.Fatalf("the day reads %d nano with %d held, want only the $1 spent before", paidDay.spent, paidDay.held)
	}
}
