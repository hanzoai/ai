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
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"
	"math/rand/v2"
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

// fixed is a quote for a call that costs n, with no completion.
func fixed(n int64) quote { return quote{fixed: n, width: 1} }

// party is who a request comes from, as the balance gate leaves them on it: the
// grant the host's usage policy answered with (nil when it said nothing), and whether
// they are owed the paid lane and find no room on it.
type party struct {
	name  string
	grant *object.LimitGrant
	full  bool
	paid  bool // the lane they are owed
}

// planGrant is a grant the host's usage policy answers with; a plan that pays has $5
// of its class left.
func planGrant(plan, pays, class string) *object.LimitGrant {
	return &object.LimitGrant{Plan: plan, Pays: pays, Class: class, State: "ok", Spend: usd(5), Settle: func(int64) {}}
}

// drained is a grant whose plan pays and has nothing of its class left.
func drained() *object.LimitGrant {
	g := planGrant("max-20x", object.PaysPlan, object.ClassPremium)
	g.Spend = 0
	return g
}

// parties are the callers every site is asked about: a Max 20x subscriber whose plan
// pays, a wallet with no plan, a guest the policy said nothing about, and the same
// subscriber with nothing of the plan left.
func parties() []party {
	return []party{
		{name: "subscriber", grant: planGrant("max-20x", object.PaysPlan, object.ClassPremium), paid: true},
		{name: "wallet without a plan", grant: planGrant("", object.PaysPrepaid, object.ClassPremium), paid: true},
		{name: "a free cap", grant: planGrant("", object.PaysFree, object.ClassOurs)},
		{name: "guest"},
		{name: "subscriber with nothing of the plan left", grant: drained(), full: true},
	}
}

// lanes makes the lanes real for one test: the platform's switch on and a book of
// its own, holding nothing.
func lanes(t *testing.T) {
	t.Helper()
	was := paidLane
	paidLane = &laneBook{}
	t.Cleanup(func() { paidLane = was })
}

// book is what every open seat on the paid lane holds.
func book() int64 {
	paidLane.mu.Lock()
	defer paidLane.mu.Unlock()
	var n int64
	for s := range paidLane.open {
		n += s.held
	}
	return n
}

// planHeld is what plan's calls in flight hold.
func planHeld(plan string) int64 {
	paidLane.mu.Lock()
	defer paidLane.mu.Unlock()
	return paidLane.plans[plan]
}

// seatAs puts c's caller on their lane as the gate does: it leaves its grant, and the
// lane is decided from it, once.
func seatAs(t *testing.T, c *ApiController, h party) *ApiController {
	t.Helper()
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
	if h.full {
		reason = ReasonFull
	}
	if got := header(c, LaneReasonHeader); got != reason {
		t.Errorf("%s = %q, want %q", LaneReasonHeader, got, reason)
	}
}

// THE LANE IS THE PLAN THAT PAYS. A paid plan's included usage puts a priced call on
// the paid lane, and so does the payer's own credit, plan or none; nothing else does —
// granted credit, the Free plan's own usage, a model's free daily cap, a free model,
// and a policy that said nothing. With the platform's switch off nobody is on it, and the response says
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
		{"a wallet with no plan", planGrant("", object.PaysPrepaid, object.ClassPremium), true},
		{"a wallet on the Free plan", planGrant("free", object.PaysPrepaid, object.ClassPremium), true},
		{"granted credit with no plan", planGrant("", object.PaysCredits, object.ClassPremium), false},
		{"granted credit with no plan, on Hanzo's own tier", planGrant("", object.PaysCredits, object.ClassOurs), false},
		{"the Free plan's included usage", planGrant("free", object.PaysPlan, object.ClassPremium), false},
		{"Pro's included usage", planGrant("dev", object.PaysPlan, object.ClassPremium), true},
		{"Max 5x's included usage", planGrant("max-5x", object.PaysPlan, object.ClassOurs), true},
		{"Max 20x's included usage", planGrant("max-20x", object.PaysPlan, object.ClassPremium), true},
		{"a Team seat on the org's pool", planGrant("team", object.PaysPlan, object.ClassPremium), true},
		{"Max 20x past its included usage, its prepaid credit", planGrant("max-20x", object.PaysPrepaid, object.ClassPremium), true},
		{"Max 20x past its included usage, its granted credit", planGrant("max-20x", object.PaysCredits, object.ClassPremium), false},
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

// THE PAID LANE HAS NO CEILING OF ITS OWN. What a payer's own wallet pays for is held
// against the wallet by its handler, never by the lane: a hundred orgs' $1,000 calls
// are all seated at once, at the ceiling each asked for, whatever the deployment says
// of its replicas. A call the plan pays is seated while the plan has room, and past
// it its payer is on the free lane, told why; a call only the paid lane serves is
// refused 413, saying the plan has less left than the call can cost. A caller never
// owed the paid lane is not told it was full.
func TestThePaidLaneHasNoCeilingOfItsOwn(t *testing.T) {
	lanes(t)
	t.Setenv("CLOUD_REPLICAS", "")
	for i := range 100 {
		org := fmt.Sprintf("org%d", i)
		s, why := paidLane.reserve(org+"\x00"+org, "", fixed(usd(1000)), math.MaxInt64)
		if s == nil {
			t.Fatalf("a wallet's $1,000 call from the %dth org was refused (%q): the paid lane caps nobody's own money", i+1, why)
		}
	}
	if held := book(); held != usd(100_000) {
		t.Fatalf("the paid lane holds %d nano for a hundred $1,000 calls, want all of it", held)
	}

	sub := party{grant: planGrant("max-20x", object.PaysPlan, object.ClassPremium), paid: true}
	if c := seatAs(t, visit(http.MethodPost, "/v1/chat/completions"), sub); FreeOnlyFor(c.Context()) {
		t.Fatal("a subscriber whose plan has room was put on the free lane")
	}
	c := seatAs(t, visit(http.MethodPost, "/v1/chat/completions"), party{grant: drained(), full: true})
	if !FreeOnlyFor(c.Context()) {
		t.Fatal("a subscriber whose plan has nothing left stayed on the paid lane")
	}
	onLane(t, c, party{full: true})
	err := laneOff(c.Context(), "vendor/x")
	if statusOf(err) != http.StatusRequestEntityTooLarge || codeOf(err) != ReasonFull || !strings.Contains(err.Error(), "your plan has left") {
		t.Fatalf("a call only the paid lane serves is refused %d %q %q, want 413 %s naming the plan", statusOf(err), codeOf(err), err, ReasonFull)
	}
	free := seatAs(t, visit(http.MethodPost, "/v1/chat/completions"), party{grant: planGrant("", object.PaysFree, object.ClassOurs)})
	onLane(t, free, party{})
	if err := laneOff(free.Context(), "vendor/x"); statusOf(err) != http.StatusServiceUnavailable {
		t.Fatalf("a caller never owed the lane is refused %d, want the switch's 503", statusOf(err))
	}
}

// A PAYER HOLDS AT MOST paidLaneCalls SEATS AT ONCE, so one payer cannot pin the lane
// with many small calls; another payer of the same org is seated beside it.
func TestAPayerHoldsAtMostItsCallsAtOnce(t *testing.T) {
	lanes(t)
	var seats []*seat
	for range paidLaneCalls + 3 {
		if s, _ := paidLane.reserve("acme\x00ann", "", fixed(usd(0.01)), math.MaxInt64); s != nil {
			seats = append(seats, s)
		}
	}
	if len(seats) != paidLaneCalls {
		t.Fatalf("one payer holds %d seats at once, want %d", len(seats), paidLaneCalls)
	}
	if s, why := paidLane.reserve("acme\x00ann", "", fixed(usd(0.01)), math.MaxInt64); s != nil || why != whyBusy {
		t.Fatalf("a payer holding its seats was seated again (%q)", why)
	}
	if s, _ := paidLane.reserve("acme\x00bob", "", fixed(usd(0.01)), math.MaxInt64); s == nil {
		t.Fatal("another payer was refused while the first held its seats")
	}
	seats[0].end()
	if s, _ := paidLane.reserve("acme\x00ann", "", fixed(usd(0.01)), math.MaxInt64); s == nil {
		t.Fatal("a payer whose call ended was not seated again")
	}
}

// A PLAN IS NEVER PASSED BY CALLS IN FLIGHT. Fifty concurrent 600 KB prompts at a
// premium model, from a payer whose plan has $0.01 of its class left: none fits, so
// none is seated and nothing is held. Then many concurrent calls from payers whose
// plans each have $3 left: every one seated held its quote before it was served, and
// what a plan's calls hold never passes its figure. Settled, nothing is held.
func TestCallsInFlightNeverPassThePlan(t *testing.T) {
	lanes(t)
	premium := strings.Repeat("x", 600_000)
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
	if held := book(); seated != 0 || held != 0 {
		t.Fatalf("%d calls seated holding %d nano for a payer with $0.01 left, want none", seated, held)
	}

	// Payers with room: four orgs, each plan with $3 left, $0.40 calls.
	const est = 400_000_000
	var seats []*seat
	var peak int64
	orgs := []string{"a", "b", "c", "d"}
	for range 100 {
		for _, org := range orgs {
			wg.Go(func() {
				plan := org + "\x00" + org + "\x00" + object.ClassPremium
				s, _ := paidLane.reserve(org+"\x00"+org, plan, fixed(est), usd(3))
				if s == nil {
					return
				}
				n := planHeld(plan)
				mu.Lock()
				seats = append(seats, s)
				peak = max(peak, n)
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	if len(seats) != 4*7 {
		t.Fatalf("%d $0.40 calls seated for four plans with $3 left, want 7 each", len(seats))
	}
	if peak > usd(3) {
		t.Fatalf("a plan's calls in flight held %d nano at their peak, past the $3 it has left", peak)
	}
	for _, s := range seats {
		wg.Go(s.settle)
	}
	wg.Wait()
	if held := book(); held != 0 {
		t.Fatalf("every call settled and %d nano is still held", held)
	}
}

// A RACE'S SEAT HOLDS UNTIL ITS ANSWER IS COUNTED. Fast mode races two providers; the
// loser is stopped at the winner's first byte and billed while the winner still
// streams, and gives nothing back: the seat holds the whole race until the winner's
// own usage settles it, once. Many such streams from one payer whose plan has room
// for four never let a fifth be seated on room a loser gave back, and when every
// answer is counted nothing is held.
func TestARacesSeatHoldsUntilItsAnswerIsCounted(t *testing.T) {
	lanes(t)
	cooled.forget()
	t.Cleanup(cooled.forget)
	quick(t)
	var mu sync.Mutex
	billed := 0
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(context.Context, object.UsageEvent) error {
		mu.Lock()
		billed++
		mu.Unlock()
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })
	r := &racers{
		after: map[string]time.Duration{"enso": 0, "do-ai": 40 * time.Millisecond},
		says: map[string][]string{
			"enso":  {"the winner answers"},
			"do-ai": {"the loser ", "got this far ", "before it was cut"},
		},
	}
	r.install(t)

	payload := []byte(`{"model":"gpt-4o","max_tokens":4096,"fast":true,"messages":[{"role":"user","content":"hi"}]}`)
	open := func(g *object.LimitGrant) *ApiController {
		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody(payload)
		Seat(c.Ctx, g, "acme", "ann", "gpt-4o")
		return c
	}
	probe := open(planGrant("max-20x", object.PaysPrepaid, object.ClassPremium))
	est := seatOf(probe.Context()).held
	seatOf(probe.Context()).end()
	const room = 4
	plan := planGrant("max-20x", object.PaysPlan, object.ClassPremium)
	plan.Spend = room*est + est/2

	var wg sync.WaitGroup
	var streams []*ApiController
	for range 2 * room {
		wg.Go(func() {
			if c := open(plan); !FreeOnlyFor(c.Context()) {
				mu.Lock()
				streams = append(streams, c)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(streams) != room {
		t.Fatalf("%d fast calls seated for a plan with room for %d at %d nano each", len(streams), room, est)
	}

	user := &iam.User{Owner: "acme", Name: "ann"}
	for _, c := range streams {
		wg.Go(func() {
			var out body
			var won *plain
			a := newAsk(route("enso", "do-ai"), nil, nil)
			a.ctx = context.WithoutCancel(c.Context())
			a.prompt = 11
			a.fan = fanOf(2, &out, &won, nil)
			a.fan.bill = c.billRaced("gpt-4o", user, false, true, "race", time.Now())
			if _, by, _, err := a.serve(); err != nil || by.name != "enso" {
				t.Errorf("served by %q (%v), want the winner", by.name, err)
			}
		})
	}
	wg.Wait()
	// Every loser is billed from its own goroutine after its race returned.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		paidLane.mu.Lock()
		waiting := 0
		for _, c := range streams {
			waiting += seatOf(c.Context()).racing
		}
		paidLane.mu.Unlock()
		if waiting == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d raced losers were never counted", waiting)
		}
	}

	mu.Lock()
	losers := billed
	mu.Unlock()
	if losers < room {
		t.Fatalf("%d losers billed, want each race's", losers)
	}
	if held := book(); held != int64(room)*est {
		t.Fatalf("with every winner still streaming the lane holds %d nano, want all %d seats' %d: a loser gave its seat back", held, room, int64(room)*est)
	}
	if late := open(plan); !FreeOnlyFor(late.Context()) {
		t.Fatal("a call was seated on room the streaming winners still hold")
	}

	for _, c := range streams {
		wg.Go(func() {
			s := seatOf(c.Context())
			s.settle()
			s.settle()
			s.end()
		})
	}
	wg.Wait()
	if held := book(); held != 0 {
		t.Fatalf("every answer counted and %d nano is still held", held)
	}
}

// A seat nothing settles gives its hold back: when its handler is done, after
// seatLapse for one a job kept, and after streamIdle for a stream whose client stopped
// taking what it writes. A kept seat outlives its handler; a stream the client keeps
// reading keeps its hold.
func TestASeatNothingSettlesGivesItsHoldBack(t *testing.T) {
	lanes(t)
	now := time.Now()
	paidLane.clock = func() time.Time { return now }
	s, _ := paidLane.reserve("acme\x00acme", "", fixed(usd(1)), math.MaxInt64)
	s.end()
	if held := book(); held != 0 {
		t.Fatalf("an ended seat still holds %d nano", held)
	}
	k, _ := paidLane.reserve("acme\x00acme", "", fixed(usd(1)), math.MaxInt64)
	k.keep()
	k.end()
	if held := book(); held != usd(1) {
		t.Fatalf("a kept seat holds %d nano after its handler, want its $1", held)
	}
	paidLane.mu.Lock()
	paidLane.lapse(now.Add(seatLapse + time.Second))
	paidLane.mu.Unlock()
	if held := book(); held != 0 {
		t.Fatalf("a seat past its lapse still holds %d nano", held)
	}

	read, _ := paidLane.reserve("acme\x00acme", "", fixed(usd(1)), math.MaxInt64)
	stalled, _ := paidLane.reserve("acme\x00acme", "", fixed(usd(1)), math.MaxInt64)
	var client bytes.Buffer
	chunk := func(w *bufio.Writer) { _, _ = w.WriteString("data: {}\n\n"); _ = w.Flush() }
	stalled.paced(chunk)(bufio.NewWriter(&client))
	for range 5 {
		now = now.Add(streamIdle / 2)
		read.paced(chunk)(bufio.NewWriter(&client))
		paidLane.mu.Lock()
		paidLane.lapse(now)
		paidLane.mu.Unlock()
	}
	if !read.open || stalled.open {
		t.Fatalf("a stream its client reads is open %v, one it stopped reading is open %v; want only the first held", read.open, stalled.open)
	}
	if client.Len() == 0 {
		t.Fatal("a paced stream wrote nothing to its client")
	}
	if held := book(); held != usd(1) {
		t.Fatalf("the lane holds %d nano, want the read stream's $1", held)
	}
}

// A PAID-LANE CALL IS BILLED, ATTRIBUTED AND SETTLED. Two members of a Team org draw
// on the org's one pool — one subject, the plan's — and each call names the member
// who made it; a call past the plan is the wallet's. Every one carries its price, and
// its usage settles its seat: nothing is held once it is counted.
func TestAPaidLaneCallIsBilledAttributedAndSettled(t *testing.T) {
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
			l.seat, _ = paidLane.reserve("acme\x00"+member, "", fixed(usd(0.01)), math.MaxInt64)
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
	call(team, "cat", false, false)
	call(team, "dan", true, true)
	if held := book(); held != 0 {
		t.Fatalf("every call was counted and the paid lane still holds %d nano", held)
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
// beaten in a race is billed after its request is over without giving the seat back;
// a video is billed when a later poll sees it done, and that settles the seat its
// create was admitted on, never a poll's or a background's.
func TestRaceLosersAndVideoJobsCountOnTheirRequestsSeat(t *testing.T) {
	lanes(t)
	var mu sync.Mutex
	billed := 0
	prev := object.UsageRecorder()
	object.SetUsageRecorder(func(context.Context, object.UsageEvent) error { mu.Lock(); billed++; mu.Unlock(); return nil })
	t.Cleanup(func() { object.SetUsageRecorder(prev) })

	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().Header.Set("X-Fast", "1")
	Seat(c.Ctx, planGrant("max-20x", object.PaysPrepaid, object.ClassPremium), "acme", "acme", "anthropic/claude-x")
	s := seatOf(c.Context())
	if s == nil {
		t.Fatal("the subscriber was not seated")
	}
	s.race(1)
	bill := c.billRaced("anthropic/claude-x", &iam.User{Owner: "acme", Name: "ann"}, true, false, "r1", time.Now())
	bill(attempt{provider: "relay-b", prompt: 1000, completion: 100, err: fmt.Errorf("beaten")})
	mu.Lock()
	losers := billed
	mu.Unlock()
	if held := book(); losers != 1 || held != s.held {
		t.Fatalf("the race's loser billed %d time(s) with %d held, want it billed and the seat's %d still held", losers, held, s.held)
	}
	s.settle()
	if held := book(); held != 0 {
		t.Fatalf("the answer was counted and %d nano is still held", held)
	}

	v := visit(http.MethodPost, "/v1/videos/generations")
	Seat(v.Ctx, planGrant("max-20x", object.PaysPrepaid, object.ClassPremium), "acme", "acme", "wan2-2-t2v-a14b")
	job := &videoJob{id: "video_lane", userModel: "wan2-2-t2v-a14b", hold: &budgetHold{}, bill: billing(v.Context()), seat: seatOf(v.Context()), createdAt: time.Now()}
	job.seat.keep()
	Unseat(v.Ctx)
	if held := book(); held != usd(0.4) {
		t.Fatalf("a video job's seat holds %d nano after its create, want its $0.40", held)
	}
	poll := visit(http.MethodGet, "/v1/videos/video_lane")
	if job.markCompleted(videoCostCents(job.userModel, 1)) {
		poll.recordVideoUsage(job.bill, &iam.User{Owner: "acme", Name: "ann"}, &object.Provider{Owner: "admin", Name: "do-ai"}, job.userModel, false, 1, "success", "", time.Now())
	}
	if held := book(); held != 0 || job.seat.open {
		t.Fatalf("the finished video left %d nano held, want the create's seat settled", held)
	}
}

// WITH THE SWITCH OFF, WHAT IS BILLED AFTER A REQUEST IS BILLED AS BEFORE. A race's
// loser is billed to the wallet on a background context, a video to the poll that sees
// it done, and a decision that names Jev is refused 503 whoever asks — before any
// question of who pays.
func TestWithTheSwitchOffLateBillsAreAsBefore(t *testing.T) {
	FreeOnly = func() bool { return true }
	t.Cleanup(func() { FreeOnly = func() bool { return false } })
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

	c := visit(http.MethodPost, "/v1/chat/completions")
	c.SetContext(context.WithValue(c.Context(), laneKey{}, &laneState{}))
	Cover(c.Ctx, planGrant("max-20x", object.PaysPlan, object.ClassPremium))
	if billing(c.Context()) != nil {
		t.Fatal("a request off the paid lane bills after it is over on its own context")
	}
	c.billRaced("gpt-4o", &iam.User{Owner: "acme", Name: "ann"}, false, false, "r1", time.Now())(attempt{provider: "relay-b", prompt: 10, completion: 1, err: fmt.Errorf("beaten")})
	mu.Lock()
	if len(events) != 1 || events[0].Plan || events[0].PaidBy != "" {
		t.Fatalf("a race loser off the paid lane was filed %+v, want the wallet's as before", events)
	}
	mu.Unlock()

	reply := decide(context.Background(), decisionCall{model: "typesafe/jev-1.13", rid: "r"})
	if reply.status != http.StatusServiceUnavailable {
		t.Fatalf("a Jev decision with the switch off and nobody paying answered %d, want the switch's 503", reply.status)
	}
}

// A CALL IS SERVED, SIZED TO WHAT ITS PLAN HAS LEFT, AND A REFUSAL SAYS WHY. A
// Claude-Code-shaped call to a $15/$75 model — max_tokens 32,000 — paid from the
// payer's own wallet is seated whole, at the ceiling it asked for, however large and
// however many are in flight beside it, up to the payer's seats: the wallet bounds it.
// Paid by a plan, it is seated whole while the plan has room for that; with less room
// it is sent with the highest ceiling that fits, saying so, and its handler sends no
// more. When even the least it is sent with does not fit what the payer's calls in
// flight leave, it is told so, 429; a call whose least is more than the plan has left,
// to send less, 413. A settled call gives its room back.
func TestACallIsServedSizedToWhatItsPlanHasLeft(t *testing.T) {
	lanes(t)
	body := func(prompt int) []byte {
		return []byte(`{"model":"claude-opus-4","max_tokens":32000,"messages":[{"role":"user","content":"` + strings.Repeat("x", prompt) + `"}]}`)
	}
	call := func(g *object.LimitGrant, payer string, prompt int) *ApiController {
		c := visit(http.MethodPost, "/v1/messages")
		c.Fiber().Request().SetBody(body(prompt))
		Seat(c.Ctx, g, "acme", payer, "claude-opus-4")
		return c
	}
	refused := func(c *ApiController, status int, says string) {
		t.Helper()
		err := laneOff(c.Context(), "claude-opus-4")
		if !FreeOnlyFor(c.Context()) || statusOf(err) != status || codeOf(err) != ReasonFull || !strings.Contains(err.Error(), says) {
			t.Fatalf("refused %d %q, want %d %s saying %q", statusOf(err), err, status, ReasonFull, says)
		}
	}

	wallet := planGrant("max-20x", object.PaysPrepaid, object.ClassPremium)
	for range paidLaneCalls {
		c := call(wallet, "ann", 1_000_000)
		if s := seatOf(c.Context()); s == nil || s.tokens != 32000 || header(c, LaneTokensHeader) != "" {
			t.Fatalf("a wallet's call with a 1 MB prompt: seat %+v, header %q; want it seated whole", s, header(c, LaneTokensHeader))
		}
	}
	refused(call(wallet, "ann", 100), http.StatusTooManyRequests, fmt.Sprintf("%d calls in flight", paidLaneCalls))

	probe := visit(http.MethodPost, "/v1/messages")
	probe.Fiber().Request().SetBody(body(100_000))
	q := quoteOf(probe.Ctx, "acme", "claude-opus-4")
	whole, least := q.at(q.ceiling), q.at(q.floor)
	if least >= whole {
		t.Fatalf("the call is quoted %d at its least and %d whole; the test wants room between them", least, whole)
	}
	plan := planGrant("max-20x", object.PaysPlan, object.ClassPremium)
	plan.Spend = whole + (least+whole)/2

	first := call(plan, "bob", 100_000)
	if s := seatOf(first.Context()); s == nil || s.tokens != 32000 || header(first, LaneTokensHeader) != "" {
		t.Fatalf("a plan's call with room for it whole: seat %+v, header %q", s, header(first, LaneTokensHeader))
	}
	sized := call(plan, "bob", 100_000)
	ss := seatOf(sized.Context())
	if ss == nil || ss.tokens >= 32000 || ss.tokens < reserveCompletionFloor {
		t.Fatalf("a call with less room than it asked for: seat %+v, want it sized below 32,000 and at least the floor", ss)
	}
	if got := header(sized, LaneTokensHeader); got != fmt.Sprint(ss.tokens) {
		t.Fatalf("%s = %q, want %d", LaneTokensHeader, got, ss.tokens)
	}
	if n := laneTokens(sized.Context(), clampMaxTokens("", 32000)); n != ss.tokens {
		t.Fatalf("the handler sends max_tokens %d, want the seat's %d", n, ss.tokens)
	}
	key := "acme\x00bob\x00" + object.ClassPremium
	if n := planHeld(key); n > plan.Spend {
		t.Fatalf("the plan's calls hold %d nano, past the %d it has left", n, plan.Spend)
	}
	refused(call(plan, "bob", 100_000), http.StatusTooManyRequests, "your calls in flight hold what your plan has left")
	refused(call(plan, "cat", 1_000_000), http.StatusRequestEntityTooLarge, "shorter prompt")

	seatOf(first.Context()).settle()
	if again := call(plan, "bob", 100_000); FreeOnlyFor(again.Context()) {
		t.Fatal("a settled call did not give its plan's room back")
	}
}

// pictureB64 is a real w×h PNG, base64: noise when noisy, which no encoder shrinks.
func pictureB64(t *testing.T, w, h int, noisy bool) string {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	if noisy {
		r := rand.New(rand.NewPCG(1, 2))
		for i := range img.Pix {
			img.Pix[i] = uint8(r.Uint32())
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// THE QUOTE IS NEVER BELOW WHAT A VENDOR CAN BILL. For every way a request was read
// cheaper than it is billed — text behind `data:`, a decodable picture with text after
// it, a remote picture, a remote or stored document, a PDF, text that tokenizes a
// token per byte, an hour-long cache write named in an escape, many choices, a
// predicted output, a vendor-defined tool — the quote at the ceiling the handler sends
// is at least the most the vendor bills for it, worked out from the vendor's own rules
// here. A real picture is read as a picture, not its bytes, and a fast call is quoted
// for both providers it races.
func TestTheQuoteIsNeverBelowWhatAVendorCanBill(t *testing.T) {
	const model = "gpt-4o"
	r := rateOf(model, "", false)
	chat := func(body string) quote {
		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody([]byte(body))
		return quoteOf(c.Ctx, "acme", model)
	}
	text := func(n int) int64 { return int64(n) * r.in } // a token per byte
	out := int64(reserveCompletionFloor) * r.out
	big := strings.Repeat("y", 600_000)
	tiny := pictureB64(t, 1, 1, false)
	whole := int64(wholeTokens) * r.in
	remotePicture := max(int64(31_218)*r.in, 7_200_000) // 11×11 tiles of 258; gpt-4o-mini's $0.0072
	for _, tc := range []struct {
		name, body string
		bound      int64
	}{
		{"600 KB of text behind data:", `{"messages":[{"role":"user","content":"data:` + big + `"}]}`, text(600_000) + out},
		{"600 KB of base64 that is no picture, in an image part", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + strings.Repeat("A", 600_000) + `"}}]}]}`, text(600_000) + out},
		{"a 1×1 picture with 600 KB after it, in a text part", `{"messages":[{"role":"user","content":[{"type":"text","text":"data:image/png;base64,` + tiny + big + `"}]}]}`, text(600_000) + out},
		{"a remote picture", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`, remotePicture + out},
		{"a remote document", `{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}}]}]}`, whole + out},
		{"a remote file", `{"messages":[{"role":"user","content":[{"type":"input_file","file_url":"https://example.com/a.pdf"}]}]}`, whole + out},
		{"a stored file", `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-abc"}}]}]}`, whole + out},
		{"an inline PDF", `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0xLjQK"}}]}]}`, whole + out},
		{"punctuation a token per byte", `{"messages":[{"role":"user","content":"` + strings.Repeat("!?", 150_000) + `"}]}`, text(300_000) + out},
		{"an hour's cache write named in an escape", `{"messages":[{"role":"user","content":[{"type":"text","text":"` + big + `","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, 2*text(600_000) + out},
		{"128 choices", `{"n":128,"messages":[{"role":"user","content":"hi"}]}`, 128 * out},
		{"a predicted output", `{"prediction":{"type":"content","content":"` + big + `"},"messages":[{"role":"user","content":"hi"}]}`, int64(600_000)*r.out + out},
		{"a vendor-defined tool", `{"tools":[{"type":"computer_20250124","name":"computer","display_width_px":1024,"display_height_px":768}],"messages":[{"role":"user","content":"hi"}]}`, text(1201) + out},
		{"a ceiling no handler sends", `{"max_tokens":10000000,"messages":[{"role":"user","content":"hi"}]}`, out},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if q := chat(tc.body); q.at(q.ceiling) < tc.bound {
				t.Fatalf("quoted %d nano, below the %d a vendor can bill", q.at(q.ceiling), tc.bound)
			}
		})
	}

	pictured := func(b64 string) quote {
		return chat(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + b64 + `"}}]}]}`)
	}
	if pic, want := pictured(pictureB64(t, 4032, 3024, false)), text(6*4*258)+out; pic.at(pic.ceiling) < want {
		t.Fatalf("a 4032×3024 photo is quoted %d nano, below its tiles' %d", pic.at(pic.ceiling), want)
	}
	noise := pictureB64(t, 1500, 1000, true)
	if pic := pictured(noise); pic.at(pic.ceiling) >= text(len(noise)) {
		t.Fatalf("a real picture is quoted as its %d encoded bytes: %d nano", len(noise), pic.at(pic.ceiling))
	}
	one := chat(`{"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody([]byte(`{"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	c.Fiber().Request().Header.Set("X-Fast", "1")
	if fast := quoteOf(c.Ctx, "acme", model); fast.at(fast.ceiling) != 2*one.at(one.ceiling) {
		t.Fatal("a fast chat is not quoted for both providers it races")
	}
	if got := quoteOf(visit(http.MethodPost, "/v1/videos/generations").Ctx, "acme", "wan2-2-t2v-a14b"); got.at(got.ceiling) != usd(0.4) {
		t.Fatalf("a video is quoted %d nano, want its $0.40 clip price", got.at(got.ceiling))
	}
}

// THE ORG'S OWN KEY IS NOT THE PLATFORM'S SPEND. While the switch is on, a call to a
// row the org holds for itself is served off the paid lane; with the switch off it is
// refused, as every priced call is.
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
	FreeOnly = func() bool { return true }
	t.Cleanup(func() { FreeOnly = func() bool { return false } })
	if !paying(ctx, mine) {
		t.Fatal("with the switch off the org's own key was served; it is refused as before")
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
// lane to a payer, a subscriber or a wallet, and to nobody else: the vendor is never
// asked for anyone else, and the payer's answer is billed, to the plan or the wallet,
// and settles its seat. A subscriber whose plan has nothing left is told so, 413,
// rather than that nothing is served.
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
					if h.full {
						want = http.StatusRequestEntityTooLarge
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
				// The plan's usage is the plan's; a wallet's call debits the wallet.
				byPlan := h.grant.Pays == object.PaysPlan
				if ev := w.paid(t); ev.Plan != byPlan || ev.USD == "" || ev.USD == "0" {
					t.Fatalf("%s's answer was filed %+v, want it billed (to the plan: %v)", h.name, ev, byPlan)
				}
				if held := book(); held != 0 {
					t.Fatalf("the subscriber's answer left %d nano held, want its seat settled", held)
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
// the subscriber with nothing of the plan left included — the free routes that stand in for
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
// its own key for a route is served by that key with nothing of the plan left; the
// platform's vendors on the route are not asked, and nothing is held on the paid lane.
// The gate knows such a call by its route (OwnKey) and never hands it to the free
// model.
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
	c := w.chatAs(t, party{grant: drained(), full: true}, `{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`, false)
	if answered(c) != http.StatusOK || mine.asked() != 1 || w.a.asked() != 0 || w.b.asked() != 0 {
		t.Fatalf("status %d, own key asked %d, platform asked %d/%d: want the org's own key to answer (%s)", answered(c), mine.asked(), w.a.asked(), w.b.asked(), sent(c))
	}
	if held := book(); held != 0 {
		t.Fatalf("the paid lane holds %d nano for a call on the org's own key", held)
	}
}

// Free users get free models: promotional credit, which nobody paid for, never seats a
// call on a paid tier of Enso, with a plan or without one; the payer's own money does.
func TestGrantedCreditNeverSeatsAPaidEnsoTier(t *testing.T) {
	lanes(t)
	for _, c := range []struct {
		name  string
		grant *object.LimitGrant
		want  Seating
	}{
		{"granted credit, no plan", &object.LimitGrant{Pays: object.PaysCredits, Class: object.ClassOurs, State: "ok", Settle: func(int64) {}}, SeatFree},
		{"a paid plan past its usage, on granted credit", &object.LimitGrant{Plan: "max-20x", Pays: object.PaysCredits, Class: object.ClassOurs, State: "near", Settle: func(int64) {}}, SeatFree},
		{"the payer's own credit, no plan", &object.LimitGrant{Pays: object.PaysPrepaid, Class: object.ClassOurs, State: "ok", Settle: func(int64) {}}, SeatPaid},
	} {
		for _, model := range []string{"enso-flash", "enso-pro", "enso-ultra"} {
			cx := visit(http.MethodPost, "/v1/chat/completions")
			Cover(cx.Ctx, c.grant)
			if got := Seat(cx.Ctx, c.grant, "acme", "acme/alice", model); got != c.want {
				t.Errorf("%s on %s: seated %v, want %v", c.name, model, got, c.want)
			}
			if seated := !FreeOnlyFor(cx.Context()); seated != (c.want == SeatPaid) {
				t.Errorf("%s on %s: on the paid lane %v", c.name, model, seated)
			}
			Unseat(cx.Ctx)
		}
	}
}
