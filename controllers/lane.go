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

// lane.go — which lane a request is served on, and what the paid lane holds for it.
//
// FreeOnly is the platform's switch: off, no request reaches a priced route. On, each
// request is served on one of two lanes, decided once, by the balance gate, from the
// grant the host's usage policy admitted it with (Seat):
//
//   - PAID: a paid plan stands behind the payer (LimitGrant.Plan — an active
//     subscription whose period was paid), something pays for this priced call — the
//     plan's included usage (PaysPlan), then the payer's prepaid or granted credit
//     (PaysPrepaid, PaysCredits) — and the call's worst case fits what the paid lane
//     has left for it.
//   - FREE: everyone else — no plan, a call nothing pays for, a free model, a payer
//     whose call does not fit, and any request that reached a handler without passing
//     the gate (a ZAP handler: the forward bridge is the ZAP route that runs the gate).
//
// WHAT A SEAT HOLDS. A call on the paid lane holds its quote (quote.go: the most it can
// cost, for every provider fast mode races it to) from admission until its answer is
// counted, against three bounds at once:
//
//   - the platform's day: PAID_LANE_DAILY, USD, default 10; 0 closes the lane, and so
//     does a process that is not known to be the only replica (CLOUD_API_REPLICAS=1);
//   - the paying org's share of that day: PAID_LANE_ORG_SHARE, a fraction, default
//     0.25, so one org cannot move every other payer to the free lane. An org with no
//     call in flight and some of its share left may seat one call larger than what is
//     left of it, up to the day's room, so a single call of any size the day can hold
//     is served;
//   - for a call the plan pays, what the plan's class has left (LimitGrant.Spend, the
//     host's figure at admission) less what the payer's calls in flight hold. A call
//     the payer's prepaid or granted credit pays is held against the wallet by its
//     handler (reserveFor), as every wallet-paid call is.
//
// A conversation whose quote at the completion ceiling it asked for does not fit is
// sent with a lower ceiling that does (X-Hanzo-Lane-Max-Tokens), never below the
// least its handler sends; one that fits at no ceiling is not seated. One payer holds
// at most paidLaneCalls seats at once.
//
// A seat is given back exactly once, when its answer is counted (recordUsage, after
// the plan's own settle). A provider fast mode raced and beat adds what it spent to the
// count and gives nothing back (seat.lost); the seat closes once its answer and every
// such loser are counted. A call that records nothing gives the hold back when its
// handler is done (Unseat). A stream that stops writing gives it back after
// streamIdle, and any seat after seatLapse. So calls in flight never pass a bound by
// more than what the calls a lapse let go spend past it.
//
// THE DAY IS KEPT IN MEMORY AND WRITTEN TO THE STORE. What each org's paid-lane calls
// spent each UTC day is counted in this process and written to its database
// (object.PaidDay) off the request path, retried until it lands, and read back the
// first time the day is asked about, so a restart or a rollout keeps the count. The
// day only moves forward; calls in flight at midnight hold on the new day.
//
// Every read of the switch on the request path asks FreeOnlyFor, never FreeOnly.
//
// While the switch is on the response says which: X-Hanzo-Lane is paid or free, and
// X-Hanzo-Lane-Reason is paid_lane_ceiling when a payer owed the paid lane had no room
// on it. A chat for a model only the paid lane serves is then answered by the free
// model in limited mode (routers' BalanceGateFilter); any other call for one is
// refused paid_lane_full (laneOff), saying why. Who pays is the grant's, unchanged by
// the lane (X-Hanzo-Paid-By).

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

const (
	// LaneHeader names the lane the request was served on: paid or free.
	LaneHeader = "X-Hanzo-Lane"
	// LaneReasonHeader says why a payer is on the free lane: ReasonCeiling.
	LaneReasonHeader = "X-Hanzo-Lane-Reason"
	// LaneTokensHeader names the completion ceiling a conversation on the paid lane is
	// sent with when it is lower than the one it asked for: the most whose worst case
	// fits what the lane had left for it.
	LaneTokensHeader = "X-Hanzo-Lane-Max-Tokens"
	// ReasonCeiling is why a payer owed the paid lane is on the free lane: the
	// platform's day, the org's share of it or the plan's allowance had no room for the
	// call.
	ReasonCeiling = "paid_lane_ceiling"
	// codeLaneFull names the refusal of a call only the paid lane serves, for a payer
	// owed the lane when it had no room for them.
	codeLaneFull = "paid_lane_full"
)

// Why the paid lane had no room for a call it was owed (laneState.why).
const (
	// whyClosed: the lane seats nobody — its day is 0, the day's count cannot be read,
	// or this process is not known to be the only replica.
	whyClosed = "closed"
	// whyLarge: the call's least worst case is more than the day, or the plan's class,
	// can ever hold for it.
	whyLarge = "large"
	// whySpent: what the day or the org's share has spent leaves no room until the next
	// UTC day.
	whySpent = "spent"
	// whyBusy: calls in flight hold the room it needs, or its payer holds
	// paidLaneCalls seats.
	whyBusy = "busy"
)

// Seating is what Seat decided for a request.
type Seating int

const (
	// SeatFree: the request is not owed the paid lane.
	SeatFree Seating = iota
	// SeatPaid: the request is on the paid lane and its quote is held.
	SeatPaid
	// SeatFull: the request is owed the paid lane, which had no room for it.
	SeatFull
)

// laneKey is where a request carries its lane (*laneState).
type laneKey struct{}

// laneState is a request's lane as the gate decided it: its seat, nil on the free
// lane, and why a payer owed one had no room ("" when it was not owed one).
type laneState struct {
	seat *seat
	why  string
}

func laneOf(ctx context.Context) *laneState {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(laneKey{}).(*laneState)
	return l
}

// seatOf is the request's seat on the paid lane, nil when it is on the free lane.
func seatOf(ctx context.Context) *seat {
	if l := laneOf(ctx); l != nil {
		return l.seat
	}
	return nil
}

// FreeOnlyFor reports whether the paid lane is closed to the request on ctx: the
// platform's switch is off, or the request holds no seat on it.
func FreeOnlyFor(ctx context.Context) bool {
	return FreeOnly() || seatOf(ctx) == nil
}

// Seat decides the lane of the request on c, once, from the grant g the balance gate
// admitted it with for model: org is the org whose ledger pays and payer its billing
// subject. A request owed the paid lane holds its quote on it, or is SeatFull when that
// does not fit. A request seated before — the gate asks again for a model it handed
// the request to — gives that seat back first. With the switch off it decides nothing
// and says nothing.
func Seat(c *zip.Ctx, g *object.LimitGrant, org, payer, model string) Seating {
	if FreeOnly() {
		return SeatFree
	}
	seatOf(c.Context()).settle(0)
	c.Fiber().Response().Header.Del(LaneTokensHeader)
	l, out := &laneState{}, SeatFree
	var q quote
	if planPays(g) {
		allow, plan := int64(math.MaxInt64), ""
		if g.Pays == object.PaysPlan {
			allow, plan = g.Spend, org+"\x00"+payer+"\x00"+g.Class
		}
		q = quoteOf(c, org, model)
		if l.seat, l.why = paidDay.reserve(org, org+"\x00"+payer, plan, q, allow); l.seat != nil {
			out = SeatPaid
		} else {
			out = SeatFull
		}
	}
	c.SetContext(context.WithValue(c.Context(), laneKey{}, l))
	switch out {
	case SeatPaid:
		c.SetHeader(LaneHeader, "paid")
		if l.seat.tokens > 0 && l.seat.tokens < q.ceiling {
			c.SetHeader(LaneTokensHeader, strconv.Itoa(l.seat.tokens))
		}
	case SeatFull:
		c.SetHeader(LaneHeader, "free")
		c.SetHeader(LaneReasonHeader, ReasonCeiling)
	default:
		c.SetHeader(LaneHeader, "free")
	}
	return out
}

// Lane says on the response that a request the gate did not seat — no policy, a policy
// that said nothing, a model it was not asked about — is on the free lane, while the
// switch is on.
func Lane(c *zip.Ctx) {
	if !FreeOnly() && laneOf(c.Context()) == nil {
		c.SetHeader(LaneHeader, "free")
	}
}

// Unseat gives back what the request's seat still holds once its handler is done: a
// whole answer has recorded what it spent by then. A stream still being written
// settles its seat from its writer (recordUsage); a refused one gives it back now.
func Unseat(c *zip.Ctx) {
	s := seatOf(c.Context())
	if s == nil {
		return
	}
	if r := c.Fiber().Response(); r.IsBodyStream() && r.StatusCode() < http.StatusBadRequest {
		return
	}
	s.end()
}

// billing is the context a usage recorded after its request is over is bound to: the
// request's values without its cancellation when it holds a seat on the paid lane, so
// the usage bills the request's grant and counts on its seat; nil off the paid lane,
// where the recorder binds its own.
func billing(ctx context.Context) context.Context {
	if seatOf(ctx) == nil {
		return nil
	}
	return context.WithoutCancel(ctx)
}

// laneTokens is n, the completion ceiling a handler sends, no higher than the one the
// request's seat on the paid lane was sized for.
func laneTokens(ctx context.Context, n int) int {
	if s := seatOf(ctx); s != nil && s.tokens > 0 {
		return min(n, s.tokens)
	}
	return n
}

// planPays reports whether a grant puts its request on the paid lane: it names a paid
// plan, the call is priced, and the plan's included usage or the payer's credit pays.
func planPays(g *object.LimitGrant) bool {
	if g == nil {
		return false
	}
	plan := strings.TrimSpace(g.Plan)
	if plan == "" || strings.EqualFold(plan, "free") || strings.EqualFold(g.Class, object.ClassFree) {
		return false
	}
	switch g.Pays {
	case object.PaysPlan, object.PaysPrepaid, object.PaysCredits, object.PaysCaller:
		return true
	}
	return false
}

// laneOff is the refusal for a call only the paid lane serves, for the request on ctx:
// paid_lane_full, saying why, when its payer was owed the lane and it had no room —
// 413 for a call it can never hold, 429 otherwise — else the switch's own refusal
// (paidLaneOff).
func laneOff(ctx context.Context, model string) error {
	l := laneOf(ctx)
	if l == nil || l.why == "" {
		return paidLaneOff(model)
	}
	status, said := http.StatusTooManyRequests, ""
	switch l.why {
	case whyClosed:
		said = "the paid lane is closed. Choose an Enso or Zen model."
	case whyLarge:
		status = http.StatusRequestEntityTooLarge
		said = "this call can cost more than the paid lane holds for one call. Send a shorter prompt or a lower max_tokens, or choose an Enso or Zen model."
	case whySpent:
		said = "the paid lane's day is used. Choose an Enso or Zen model, or try again after 00:00 UTC."
	default:
		said = "the paid lane is full right now. Choose an Enso or Zen model, or try again in a few minutes."
	}
	return &apiError{status: status, code: codeLaneFull, msg: fmt.Sprintf("model %q is not being served: %s", model, said)}
}

// shut reports whether the paid lane is closed to a call to p for the request on ctx:
// closed to the request, unless p is the caller's org's own key (own) while the
// platform's switch is on. An org's own key spends the org's money, never the
// platform's.
func shut(ctx context.Context, p *object.Provider) bool {
	return FreeOnlyFor(ctx) && (FreeOnly() || !own(p))
}

// own reports whether p is a row an org holds for itself — its own key — rather than
// one of the platform's.
func own(p *object.Provider) bool {
	return p != nil && p.Owner != "" && p.Owner != globalProviderOwner
}

// OwnKey reports whether org serves model with a key of its own: the provider its
// route names is a row org holds (BYOK, ownRow). The paid lane neither holds for such
// a call nor hands it to another model.
func OwnKey(org, model string) bool {
	r := resolveModelRoute(strings.ToLower(strings.TrimSpace(model)))
	return r != nil && ownRow(org, r.providerName)
}

// ── the platform's day ──────────────────────────────────────────────────────

const (
	// paidLaneDefault is the platform's paid-lane day when PAID_LANE_DAILY is unset, in
	// nano-dollars: $10.
	paidLaneDefault = 10 * 1_000_000_000
	// paidLaneMaxUSD is the most PAID_LANE_DAILY can state: every amount up to it is a
	// whole number of nano-dollars an int64 holds, on every CPU.
	paidLaneMaxUSD = 1e9
	// paidLaneShare is the most of the platform's day one org's calls may hold and
	// spend when PAID_LANE_ORG_SHARE is unset.
	paidLaneShare = 0.25
	// paidLaneCalls is the most seats one payer holds at once.
	paidLaneCalls = 8
	// seatLapse is the longest a seat holds when nothing settles it: a whole answer
	// whose handler never finished, a job nobody polled to its end.
	seatLapse = 30 * time.Minute
	// streamIdle is how long a stream holds once its client stops taking what it
	// writes.
	streamIdle = 2 * time.Minute
	// loadRetry is how often a day whose count could not be read is asked for again.
	loadRetry = 5 * time.Second
	// keptDays is how many past days the store keeps.
	keptDays = 7
)

// paidLaneDaily is what every paid-lane call together may spend in a UTC day, in
// nano-dollars. PAID_LANE_DAILY states it in USD; 0 closes the paid lane, and a value
// that is not a finite amount above 0 and at most paidLaneMaxUSD closes it too. The
// day's holds and count live in this process, so the lane is closed unless
// CLOUD_API_REPLICAS states this process is the only replica: two would each hold a
// whole day.
func paidLaneDaily() int64 {
	if n, ok := object.SinglePodReplicaHint(); !ok || n != 1 {
		return 0
	}
	s := strings.TrimSpace(conf.GetConfigString("PAID_LANE_DAILY"))
	if s == "" {
		return paidLaneDefault
	}
	usd, err := strconv.ParseFloat(s, 64)
	if err != nil || !(usd > 0) || usd > paidLaneMaxUSD {
		return 0
	}
	return usdToNano(usd)
}

// orgShare is the most one org's calls may hold and spend of a day of ceiling, in
// nano-dollars. PAID_LANE_ORG_SHARE states it as a fraction of the day; a value that
// is not above 0 and at most 1 closes the lane.
func orgShare(ceiling int64) int64 {
	f := paidLaneShare
	if s := strings.TrimSpace(conf.GetConfigString("PAID_LANE_ORG_SHARE")); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || !(v > 0) || v > 1 {
			return 0
		}
		f = v
	}
	return min(int64(float64(ceiling)*f), ceiling)
}

// laneBook is the paid lane's current UTC day: what was spent, what calls in flight
// hold, each org's share of both, each plan payer's holds and each payer's seats. mu
// guards every field below it but the writer's own (kick, writer, wmu).
type laneBook struct {
	clock  func() time.Time // the time; time.Now when nil
	mu     sync.Mutex
	day    string
	loaded bool      // the store's count for day was read
	tried  time.Time // when that read last failed
	spent  int64
	held   int64
	orgs   map[string]*laneUse
	plans  map[string]int64
	calls  map[string]int
	open   map[*seat]struct{}

	// store says the day is written to the store (object.PaidDay). pending is what was
	// counted and is not written yet, by day and org; the writer (write) takes it to
	// the store off the request path, one flush at a time (wmu).
	store   bool
	pending map[dayOrg]int64
	kick    chan struct{}
	writer  sync.Once
	wmu     sync.Mutex
	dropped string // the day past days were last dropped on
}

// laneUse is one org's spend and holds on the day.
type laneUse struct{ spent, held int64 }

// dayOrg keys what an org spent on a day.
type dayOrg struct{ day, org string }

// paidDay is the platform's paid-lane day.
var paidDay = &laneBook{store: true}

// seat is one call's place on the paid lane: what it holds, against which org's share,
// which payer's seats and which plan payer, since when. Every field but last is
// guarded by book.mu.
type seat struct {
	book   *laneBook
	org    string
	caller string // the payer whose seats it counts against: org and subject
	plan   string // the plan payer whose allowance it holds against, "" when the plan does not pay
	held   int64
	tokens int // the completion ceiling it was sized for; 0 for a call with none
	at     time.Time
	// last is when the client last took what the call's stream wrote, in Unix
	// nanoseconds; 0 for a call that does not stream.
	last atomic.Int64
	// open: it holds. kept: a job outlives its request (keep). done: its answer was
	// counted or its handler is done. racing: providers it raced that lost and are not
	// counted yet.
	open, kept, done bool
	racing           int
}

func (b *laneBook) now() time.Time {
	if b.clock != nil {
		return b.clock()
	}
	return time.Now()
}

// reserve seats a call for org quoted q, for caller — the payer whose seats it counts
// against — and, for a call a plan pays, against plan's allow less what its calls in
// flight hold. It fits the day, the org's share and the plan; a conversation is sized
// down to the highest completion ceiling that fits (quote.fit). Nil and why when it
// does not fit.
func (b *laneBook) reserve(org, caller, plan string, q quote, allow int64) (*seat, string) {
	ceiling := paidLaneDaily()
	share := orgShare(ceiling)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.turn(now)
	if ceiling <= 0 || share <= 0 || !b.load(now) {
		return nil, whyClosed
	}
	b.lapse(now)
	least := q.at(q.floor)
	if least > ceiling || (plan != "" && least > allow) {
		return nil, whyLarge
	}
	u := b.use(org)
	day := ceiling - b.spent
	if least > day || u.spent >= share {
		return nil, whySpent
	}
	room := day - b.held
	if u.held > 0 {
		room = min(room, share-u.spent-u.held)
	}
	if plan != "" {
		room = min(room, allow-b.plans[plan])
	}
	tokens, ok := q.fit(room)
	if !ok || b.calls[caller] >= paidLaneCalls {
		return nil, whyBusy
	}
	s := &seat{book: b, org: org, caller: caller, plan: plan, held: q.at(tokens), at: now, open: true}
	if q.ceiling > 0 {
		s.tokens = tokens
	}
	b.held += s.held
	u.held += s.held
	if plan != "" {
		b.plans[plan] += s.held
	}
	b.calls[caller]++
	b.open[s] = struct{}{}
	return s, ""
}

// turn moves the book to now's UTC day, never back to an earlier one. A new day starts
// with nothing spent but what the store holds for it (load); what calls in flight hold
// is carried into it.
func (b *laneBook) turn(now time.Time) {
	if b.open == nil {
		b.orgs, b.plans, b.calls = map[string]*laneUse{}, map[string]int64{}, map[string]int{}
		b.open, b.pending = map[*seat]struct{}{}, map[dayOrg]int64{}
	}
	day := utcDay(now)
	if day <= b.day {
		return
	}
	b.day, b.loaded, b.tried, b.spent = day, false, time.Time{}, 0
	for org, u := range b.orgs {
		if u.spent = 0; u.held == 0 {
			delete(b.orgs, org)
		}
	}
}

// load adds what the store holds for the book's day, once per day: what this process
// counted before it started and what any before it spent. It reports false while that
// read fails, asking again at most every loadRetry: a day whose count cannot be read
// seats nobody. Nothing of the day is written before it is read (flush), so nothing is
// counted twice.
func (b *laneBook) load(now time.Time) bool {
	if b.loaded {
		return true
	}
	if b.store {
		if !b.tried.IsZero() && now.Sub(b.tried) < loadRetry {
			return false
		}
		kept, err := object.PaidDays(b.day)
		if err != nil {
			b.tried = now
			log.Error("paid lane: the day's count could not be read; nobody is seated: %v", err)
			return false
		}
		for org, n := range kept {
			b.use(org).spent += n
			b.spent += n
		}
	}
	b.loaded = true
	b.wake()
	return true
}

// count adds n nano-dollars of org's spend to the day, to be written to the store.
func (b *laneBook) count(org string, n int64) {
	if n <= 0 {
		return
	}
	b.use(org).spent += n
	b.spent += n
	if b.store {
		b.pending[dayOrg{b.day, org}] += n
		b.wake()
	}
}

// close gives back what an open seat holds.
func (b *laneBook) close(s *seat) {
	if !s.open {
		return
	}
	s.open = false
	delete(b.open, s)
	b.held -= s.held
	b.use(s.org).held -= s.held
	if s.plan != "" {
		if b.plans[s.plan] -= s.held; b.plans[s.plan] <= 0 {
			delete(b.plans, s.plan)
		}
	}
	if b.calls[s.caller]--; b.calls[s.caller] <= 0 {
		delete(b.calls, s.caller)
	}
}

// lapse closes every seat held past seatLapse, and every stream whose client has taken
// nothing for streamIdle.
func (b *laneBook) lapse(now time.Time) {
	for s := range b.open {
		idle := now.Sub(s.at) > seatLapse
		if t := s.last.Load(); t != 0 && now.Sub(time.Unix(0, t)) > streamIdle {
			idle = true
		}
		if idle {
			b.close(s)
		}
	}
}

func (b *laneBook) use(org string) *laneUse {
	u := b.orgs[org]
	if u == nil {
		u = &laneUse{}
		b.orgs[org] = u
	}
	return u
}

// ── the store ────────────────────────────────────────────────────────────────

// wake has the writer write what is pending. The writer runs off the request path,
// started the first time anything is pending.
func (b *laneBook) wake() {
	if !b.store || len(b.pending) == 0 {
		return
	}
	b.writer.Do(func() {
		b.kick = make(chan struct{}, 1)
		go b.write()
	})
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// write flushes each time it is woken, and again after a growing pause while a flush
// fails, until it lands.
func (b *laneBook) write() {
	for range b.kick {
		for wait := time.Second; b.flush(false) != nil; wait = min(2*wait, time.Minute) {
			time.Sleep(wait)
		}
	}
}

// flush writes what is pending to the store, a row per day and org, and puts back what
// could not be written. A day not yet read (load) is left pending unless all says to
// write everything, as a process that stops does; it is then read with it in.
func (b *laneBook) flush(all bool) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	b.mu.Lock()
	batch := map[dayOrg]int64{}
	for k, n := range b.pending {
		if all || k.day < b.day || b.loaded {
			batch[k] = n
			delete(b.pending, k)
		}
	}
	drop := ""
	if b.loaded && b.dropped != b.day {
		drop = utcDay(b.now().AddDate(0, 0, -keptDays))
	}
	day := b.day
	b.mu.Unlock()
	var failed error
	for k, n := range batch {
		if err := object.CountPaidDay(k.day, k.org, n); err != nil {
			failed = err
			b.mu.Lock()
			b.pending[k] += n
			b.mu.Unlock()
		}
	}
	if failed != nil {
		log.Error("paid lane: spend could not be written to the store; it is kept in memory and written again: %v", failed)
		return failed
	}
	if drop != "" {
		if err := object.DropPaidDays(drop); err != nil {
			log.Warn("paid lane: past days could not be dropped: %v", err)
		} else {
			b.mu.Lock()
			b.dropped = day
			b.mu.Unlock()
		}
	}
	return nil
}

// drain writes everything pending before ctx ends: what a process that stops owes the
// store.
func (b *laneBook) drain(ctx context.Context) error {
	if !b.store {
		return nil
	}
	for wait := 100 * time.Millisecond; ; wait = min(2*wait, time.Second) {
		if b.flush(true) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// ── a seat ───────────────────────────────────────────────────────────────────

// settle counts n nano-dollars the call's answer spent and gives back what the seat
// holds, once every provider it raced is counted too. Only the first settle closes it;
// a later one only counts.
func (s *seat) settle(n int64) {
	if s == nil {
		return
	}
	b := s.book
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turn(b.now())
	b.count(s.org, n)
	s.done = true
	if s.racing <= 0 {
		b.close(s)
	}
}

// race says n more providers are raced for the call: each is counted by lost, and the
// seat holds until they are.
func (s *seat) race(n int) {
	if s == nil {
		return
	}
	s.book.mu.Lock()
	s.racing += n
	s.book.mu.Unlock()
}

// lost counts n nano-dollars a raced provider that did not serve the answer spent. It
// gives nothing back: the seat closes once the answer and every loser are counted.
func (s *seat) lost(n int64) {
	if s == nil {
		return
	}
	b := s.book
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turn(b.now())
	b.count(s.org, n)
	if s.racing--; s.racing <= 0 && s.done {
		b.close(s)
	}
}

// end gives back what the seat holds when its handler is done, unless a job kept it
// (keep) or a raced provider is still to be counted.
func (s *seat) end() {
	if s == nil {
		return
	}
	s.book.mu.Lock()
	defer s.book.mu.Unlock()
	if s.kept {
		return
	}
	s.done = true
	if s.racing <= 0 {
		s.book.close(s)
	}
}

// keep hands the seat to a job that outlives its request: the hold stays until the
// job's usage settles it, it is dropped (settle 0), or it lapses.
func (s *seat) keep() {
	if s == nil {
		return
	}
	s.book.mu.Lock()
	s.kept = true
	s.book.mu.Unlock()
}

// touch says the client took what the call's stream wrote.
func (s *seat) touch() { s.last.Store(s.book.now().UnixNano()) }

// paced is fn writing a stream through a writer that touches the seat each time the
// client takes a chunk, so a stream nobody reads gives its hold back (lapse). fn
// itself off the paid lane.
func (s *seat) paced(fn func(*bufio.Writer)) func(*bufio.Writer) {
	if s == nil {
		return fn
	}
	return func(bw *bufio.Writer) {
		s.touch()
		w := bufio.NewWriter(pace{bw, s})
		fn(w)
		_ = w.Flush()
	}
}

// pace sends each chunk on to the client at once and touches its seat once it went.
type pace struct {
	w *bufio.Writer
	s *seat
}

func (p pace) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	if err == nil {
		err = p.w.Flush()
	}
	if err == nil {
		p.s.touch()
	}
	return n, err
}

// spendCap is the most a Hanzo family may spend on paid upstream for a covered request
// on seat s: the plan's figure, never more than the seat holds. Nothing off the paid
// lane.
func spendCap(g *object.LimitGrant, s *seat) int64 {
	if g == nil || s == nil {
		return 0
	}
	return min(g.Spend, s.held)
}

// laneSpend is what a call on the paid lane counts against the day, in nano-dollars:
// the most of its price, its cost and what it spent of a plan, so a model sold below
// its cost, or at zero on a plan, counts what it cost. A call on the org's own key
// spent nothing of the platform's.
func laneSpend(r *usageRecord) int64 {
	if r.BYO {
		return 0
	}
	m := usageMargin(r)
	n := max(m.BilledNano, r.planNanos)
	if m.CostNano != nil {
		n = max(n, *m.CostNano)
	}
	return n
}
