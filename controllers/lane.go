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
//   - PAID: something pays for this priced call — a paid plan's included usage
//     (PaysPlan, LimitGrant.Plan an active subscription whose period was paid), or the
//     payer's own cash or the org's credit line, with a plan or without one
//     (PaysPrepaid) — and the call's worst case fits what its payer has.
//   - FREE: everyone else — a call nothing pays for, a call granted (promotional)
//     credit pays for, a free model, a payer whose call does not fit, and any request
//     that reached a handler without passing the gate (a ZAP handler: the forward
//     bridge is the ZAP route that runs the gate).
//
// THE PAID LANE HAS NO CEILING OF ITS OWN. A payer spends what they bought — a plan's
// included usage, their prepaid cash, the org's credit line — and nothing caps what
// paid-lane calls spend together, or what one org's calls spend. A call on the paid
// lane holds its quote (quote.go: the most it can cost, for every provider fast mode
// races it to) from admission until its answer is counted, against what its payer
// has:
//
//   - a call the plan pays: what the plan's class has left (LimitGrant.Spend, the
//     host's figure at admission) less what the payer's calls in flight hold;
//   - a call the payer's prepaid cash or the org's credit pays: the wallet, held by its
//     handler (reserveFor), as every wallet-paid call is.
//
// A conversation the plan pays whose quote at the completion ceiling it asked for does
// not fit is sent with a lower ceiling that does (X-Hanzo-Lane-Max-Tokens), never
// below the least its handler sends; one that fits at no ceiling is not seated. A
// payer's plan pays for at most paidLaneCalls of its calls at once. A call the payer's
// own money pays is never refused a seat: its wallet is what bounds it.
//
// A seat is given back exactly once, when its answer is counted (recordUsage, after
// the plan's own settle). A provider fast mode raced and beat gives nothing back
// (seat.lost); the seat closes once its answer and every such loser are counted. A
// call that records nothing gives the hold back when its handler is done (Unseat). A
// stream that stops writing gives it back after streamIdle, and any seat after
// seatLapse. So the calls a plan pays for in flight never pass what it has left by
// more than what the calls a lapse let go spend past it.
//
// THE HOLDS ARE THIS PROCESS'S, as the wallet's are (object.GlobalBalanceLedger): both
// are right at one replica only, which the deployment states (CLOUD_REPLICAS) and
// bootstrap.go holds it to, refusing to start at more.
//
// Every read of the switch on the request path asks FreeOnlyFor, never FreeOnly.
//
// While the switch is on the response says which: X-Hanzo-Lane is paid or free, and
// X-Hanzo-Lane-Reason is paid_lane_full when a payer owed the paid lane had no room on
// it. A chat for a model only the paid lane serves is then answered by the free model
// in limited mode (routers' BalanceGateFilter); any other call for one is refused
// paid_lane_full (laneOff), saying why. Who pays is the grant's, unchanged by the lane
// (X-Hanzo-Paid-By).

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

	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

const (
	// LaneHeader names the lane the request was served on: paid or free.
	LaneHeader = "X-Hanzo-Lane"
	// LaneReasonHeader says why a payer is on the free lane: ReasonFull.
	LaneReasonHeader = "X-Hanzo-Lane-Reason"
	// LaneTokensHeader names the completion ceiling a conversation on the paid lane is
	// sent with when it is lower than the one it asked for: the most whose worst case
	// fits what its plan had left for it.
	LaneTokensHeader = "X-Hanzo-Lane-Max-Tokens"
	// ReasonFull is why a payer owed the paid lane is on the free lane — what its plan
	// has left, or its seats, had no room for the call — and the code a call only the
	// paid lane serves is refused with then.
	ReasonFull = "paid_lane_full"
)

// Why the paid lane had no room for a call it was owed (laneState.why).
const (
	// whyLarge: the call's least worst case is more than the plan's class has left.
	whyLarge = "large"
	// whyBusy: the payer's calls in flight hold what the plan's class has left, or its
	// plan pays for paidLaneCalls of them.
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
	seatOf(c.Context()).settle()
	c.Fiber().Response().Header.Del(LaneTokensHeader)
	l, out := &laneState{}, SeatFree
	var q quote
	if pays(g) {
		allow, plan := int64(math.MaxInt64), ""
		if g.Pays == object.PaysPlan {
			allow, plan = g.Spend, org+"\x00"+payer+"\x00"+g.Class
		}
		q = quoteOf(c, org, model)
		if l.seat, l.why = paidLane.reserve(org+"\x00"+payer, plan, q, allow); l.seat != nil {
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
		c.SetHeader(LaneReasonHeader, ReasonFull)
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
// the usage bills the request's grant and settles its seat; nil off the paid lane,
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

// pays reports whether a grant puts its request on the paid lane: the call is priced
// and money the payer bought pays for it — a paid plan's included usage, the payer's
// own cash or the org's credit line (which the policy reports as prepaid), plan or
// none, or the program that made the call in this process (PaysCaller), which asks
// for its customer's plan before it seats (routers callerPays).
//
// GRANTED CREDIT NEVER SEATS. It is promotional: nobody paid for it, so a caller whose
// call it would pay is a free user, and free users get free models, on every priced
// model, Hanzo's own tiers and third-party ones alike, whatever the host's policy lets
// credit pay for. A granted-credit caller stays on the free lane, where a Hanzo family
// answers from its free models.
func pays(g *object.LimitGrant) bool {
	if g == nil || strings.EqualFold(g.Class, object.ClassFree) {
		return false
	}
	switch g.Pays {
	case object.PaysPrepaid, object.PaysCaller:
		return true
	case object.PaysPlan:
		plan := strings.TrimSpace(g.Plan)
		return plan != "" && !strings.EqualFold(plan, "free")
	}
	return false
}

// laneOff is the refusal for a call only the paid lane serves, for the request on ctx:
// paid_lane_full, saying why, when its payer was owed the lane and it had no room —
// 413 for a call whose least is more than its plan has left, 429 otherwise — else the
// switch's own refusal (paidLaneOff).
func laneOff(ctx context.Context, model string) error {
	l := laneOf(ctx)
	if l == nil || l.why == "" {
		return paidLaneOff(model)
	}
	status, said := http.StatusTooManyRequests, ""
	switch l.why {
	case whyLarge:
		status = http.StatusRequestEntityTooLarge
		said = "this call can cost more than your plan has left. Send a shorter prompt or a lower max_tokens, or choose an Enso or Zen model."
	default:
		said = fmt.Sprintf("your calls in flight hold what your plan has left, or your plan is paying for %d calls in flight. Choose an Enso or Zen model, or try again when one of them is answered.", paidLaneCalls)
	}
	return &apiError{status: status, code: ReasonFull, msg: fmt.Sprintf("model %q is not being served: %s", model, said)}
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

// ── the book ─────────────────────────────────────────────────────────────────

const (
	// paidLaneCalls is the most of one payer's calls its plan pays for at once.
	paidLaneCalls = 8
	// seatLapse is the longest a seat holds when nothing settles it: a whole answer
	// whose handler never finished, a job nobody polled to its end.
	seatLapse = 30 * time.Minute
	// streamIdle is how long a stream holds once its client stops taking what it
	// writes.
	streamIdle = 2 * time.Minute
)

// laneBook is what the paid lane holds: each plan payer's holds, how many of each
// payer's calls its plan pays for, and every open seat. mu guards every field below it.
type laneBook struct {
	clock func() time.Time // the time; time.Now when nil
	mu    sync.Mutex
	plans map[string]int64
	calls map[string]int
	open  map[*seat]struct{}
}

// paidLane is the paid lane's book.
var paidLane = &laneBook{}

// seat is one call's place on the paid lane: what it holds, for which payer and
// against which plan payer, since when. Every field but last is guarded by book.mu.
type seat struct {
	book   *laneBook
	caller string // the payer: org and subject
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

// reserve seats a call quoted q for caller, the payer, and, for a call a plan pays,
// against plan's allow less what its calls in flight hold and as one of the
// paidLaneCalls the plan pays for at once; a conversation is sized down to the highest
// completion ceiling that fits (quote.fit). A call no plan pays — the payer's own cash,
// the org's credit line — is always seated: its wallet is held by its handler
// (reserveFor), and that is its only bound. Nil and why when it does not fit.
func (b *laneBook) reserve(caller, plan string, q quote, allow int64) (*seat, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open == nil {
		b.plans, b.calls, b.open = map[string]int64{}, map[string]int{}, map[*seat]struct{}{}
	}
	now := b.now()
	b.lapse(now)
	room := int64(math.MaxInt64)
	if plan != "" {
		if q.at(q.floor) > allow {
			return nil, whyLarge
		}
		room = allow - b.plans[plan]
	}
	tokens, ok := q.fit(room)
	if !ok || plan != "" && b.calls[caller] >= paidLaneCalls {
		return nil, whyBusy
	}
	s := &seat{book: b, caller: caller, plan: plan, held: q.at(tokens), at: now, open: true}
	if q.ceiling > 0 {
		s.tokens = tokens
	}
	if plan != "" {
		b.plans[plan] += s.held
		b.calls[caller]++
	}
	b.open[s] = struct{}{}
	return s, ""
}

// close gives back what an open seat holds.
func (b *laneBook) close(s *seat) {
	if !s.open {
		return
	}
	s.open = false
	delete(b.open, s)
	if s.plan != "" {
		if b.plans[s.plan] -= s.held; b.plans[s.plan] <= 0 {
			delete(b.plans, s.plan)
		}
		if b.calls[s.caller]--; b.calls[s.caller] <= 0 {
			delete(b.calls, s.caller)
		}
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

// ── a seat ───────────────────────────────────────────────────────────────────

// settle says the call's answer is counted, and gives back what the seat holds once
// every provider it raced is counted too. Only the first settle closes it.
func (s *seat) settle() {
	if s == nil {
		return
	}
	s.book.mu.Lock()
	defer s.book.mu.Unlock()
	s.done = true
	if s.racing <= 0 {
		s.book.close(s)
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

// lost says a raced provider that did not serve the answer is counted. It gives
// nothing back: the seat closes once the answer and every loser are counted.
func (s *seat) lost() {
	if s == nil {
		return
	}
	s.book.mu.Lock()
	defer s.book.mu.Unlock()
	if s.racing--; s.racing <= 0 && s.done {
		s.book.close(s)
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
// job's usage settles it, it is dropped (settle), or it lapses.
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
