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
// WHAT A SEAT HOLDS. A call on the paid lane holds its estimate (estimate: its prompt
// and its completion ceiling at the most of the model's list price and its cost, for
// every provider fast mode races it to) from admission until its usage is recorded,
// against three bounds at once:
//
//   - the platform's day: PAID_LANE_DAILY, USD, default 10; 0 closes the lane;
//   - the paying org's share of that day: PAID_LANE_ORG_SHARE, a fraction, default
//     0.25, so one org cannot move every other payer to the free lane;
//   - for a call the plan pays, what the plan's class has left (LimitGrant.Spend, the
//     host's figure at admission) less what the payer's calls in flight hold. A call
//     the payer's prepaid or granted credit pays is held against the wallet by its
//     handler (reserveFor), as every wallet-paid call is.
//
// A call that does not fit is not seated: a payer owed the paid lane is on the free
// lane, told why. Its usage record settles a seat at what the call spent (laneSpend),
// after the plan's own settle; a call that records nothing gives the hold back when
// its handler is done (Unseat); a stream or a job that never records gives it back
// after seatLapse. So in-flight calls together never pass a bound by more than what
// one call spends past its estimate.
//
// THE DAY IS KEPT IN THE STORE. What each org's paid-lane calls spent each UTC day is
// written to this process's database (object.PaidDay) as it settles and read back the
// first time the day is asked about, so a restart or a rollout keeps the count. The
// holds are this process's: ai runs as one replica (bootstrap's single-pod invariant).
//
// Every read of the switch on the request path asks FreeOnlyFor, never FreeOnly.
//
// While the switch is on the response says which: X-Hanzo-Lane is paid or free, and
// X-Hanzo-Lane-Reason is paid_lane_ceiling when a payer owed the paid lane had no room
// on it. A chat for a model only the paid lane serves is then answered by the free
// model in limited mode (routers' BalanceGateFilter); any other call for one is
// refused 429 paid_lane_full (laneOff). Who pays is the grant's, unchanged by the lane
// (X-Hanzo-Paid-By).

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	// ReasonCeiling is why a payer owed the paid lane is on the free lane: the
	// platform's day, the org's share of it or the plan's allowance had no room for the
	// call.
	ReasonCeiling = "paid_lane_ceiling"
	// codeLaneFull names the refusal of a call only the paid lane serves, for a payer
	// owed the lane when it had no room for them.
	codeLaneFull = "paid_lane_full"
)

// Seating is what Seat decided for a request.
type Seating int

const (
	// SeatFree: the request is not owed the paid lane.
	SeatFree Seating = iota
	// SeatPaid: the request is on the paid lane and its estimate is held.
	SeatPaid
	// SeatFull: the request is owed the paid lane, which had no room for it.
	SeatFull
)

// laneKey is where a request carries its lane (*laneState).
type laneKey struct{}

// laneState is a request's lane as the gate decided it: its seat, nil on the free lane, and
// whether it was owed one that had no room.
type laneState struct {
	seat *seat
	full bool
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
// subject. A request owed the paid lane holds its estimate on it, or is SeatFull when
// that does not fit. A request seated before — the gate asks again for a model it
// handed the request to — gives that seat back first. With the switch off it decides
// nothing and says nothing.
func Seat(c *zip.Ctx, g *object.LimitGrant, org, payer, model string) Seating {
	if FreeOnly() {
		return SeatFree
	}
	seatOf(c.Context()).settle(0)
	l, out := &laneState{}, SeatFree
	if planPays(g) {
		allow, who := int64(math.MaxInt64), ""
		if g.Pays == object.PaysPlan {
			allow, who = g.Spend, org+"\x00"+payer+"\x00"+g.Class
		}
		if s := paidDay.reserve(org, who, estimate(c, model), allow, time.Now()); s != nil {
			l.seat, out = s, SeatPaid
		} else {
			l.full, out = true, SeatFull
		}
	}
	c.SetContext(context.WithValue(c.Context(), laneKey{}, l))
	if out == SeatPaid {
		c.SetHeader(LaneHeader, "paid")
	} else {
		c.SetHeader(LaneHeader, "free")
	}
	if out == SeatFull {
		c.SetHeader(LaneReasonHeader, ReasonCeiling)
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
	case object.PaysPlan, object.PaysPrepaid, object.PaysCredits:
		return true
	}
	return false
}

// laneOff is the refusal for a call only the paid lane serves, for the request on ctx:
// 429 paid_lane_full when its payer was owed the lane and it had no room, else the
// switch's own refusal (paidLaneOff).
func laneOff(ctx context.Context, model string) error {
	if l := laneOf(ctx); l != nil && l.full {
		return &apiError{status: http.StatusTooManyRequests, code: codeLaneFull,
			msg: fmt.Sprintf("model %q is not being served right now: the paid lane is full. Choose an Enso or Zen model, or try again after 00:00 UTC.", model)}
	}
	return paidLaneOff(model)
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

// paidLaneDefault is the platform's paid-lane day when PAID_LANE_DAILY is unset, in
// nano-dollars: $10.
const paidLaneDefault = 10 * 1_000_000_000

// paidLaneMaxUSD is the most PAID_LANE_DAILY can state: every amount up to it is a
// whole number of nano-dollars an int64 holds, on every CPU.
const paidLaneMaxUSD = 1e9

// paidLaneShare is the most of the platform's day one org's calls may hold and spend
// when PAID_LANE_ORG_SHARE is unset.
const paidLaneShare = 0.25

// paidLaneDaily is what every paid-lane call together may spend in a UTC day, in
// nano-dollars. PAID_LANE_DAILY states it in USD; 0 closes the paid lane, and a value
// that is not a finite amount above 0 and at most paidLaneMaxUSD closes it too.
func paidLaneDaily() int64 {
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

// seatLapse is how long a seat holds its estimate when nothing settles it: a stream
// whose writer never recorded, a job nobody polled to its end.
const seatLapse = 30 * time.Minute

// laneBook is one UTC day of the paid lane: what was spent, what calls in flight hold,
// each org's share of both, and each plan payer's holds.
type laneBook struct {
	mu     sync.Mutex
	day    string
	loaded bool
	spent  int64
	held   int64
	orgs   map[string]*laneUse
	payers map[string]int64
	open   map[*seat]struct{}
	// store says the day is kept in the store (object.PaidDay); wmu makes one write at
	// a time.
	store bool
	wmu   sync.Mutex
}

// laneUse is one org's spend and holds on the day.
type laneUse struct{ spent, held int64 }

// paidDay is the platform's paid-lane day.
var paidDay = &laneBook{store: true}

// seat is one call's place on the paid lane: what it holds, against which org's share
// and which plan payer, since when. open and kept are guarded by book.mu.
type seat struct {
	book       *laneBook
	day        string
	org, payer string
	held       int64
	at         time.Time
	open, kept bool
}

// reserve seats a call for org that may cost est: it fits the day, the org's share and,
// for a payer the plan pays, allow less what the payer's calls in flight hold. Nil when
// it does not fit, or when the day's count cannot be read.
func (b *laneBook) reserve(org, payer string, est, allow int64, now time.Time) *seat {
	ceiling := paidLaneDaily()
	share := orgShare(ceiling)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.roll(now) {
		return nil
	}
	b.lapse(now)
	u := b.use(org)
	if est > ceiling-b.spent-b.held || est > share-u.spent-u.held || (payer != "" && est > allow-b.payers[payer]) {
		return nil
	}
	s := &seat{book: b, day: b.day, org: org, payer: payer, held: est, at: now, open: true}
	b.held += est
	u.held += est
	if payer != "" {
		b.payers[payer] += est
	}
	b.open[s] = struct{}{}
	return s
}

// settle gives back what s holds and counts n nano-dollars of spend against the day
// and its org. A seat settles its hold once; every later settle only counts.
func (b *laneBook) settle(s *seat, n int64, now time.Time) {
	b.mu.Lock()
	b.close(s)
	day := utcDay(now)
	if n > 0 && b.roll(now) {
		b.use(s.org).spent += n
		b.spent += n
	}
	b.mu.Unlock()
	if n > 0 && b.store {
		b.write(day, s.org, n)
	}
}

// roll turns the book to now's UTC day and reads what the store holds for it the
// first time the day is asked about. It reports false while that read fails: a day
// whose count cannot be read seats nobody.
func (b *laneBook) roll(now time.Time) bool {
	day := utcDay(now)
	if b.day != day {
		b.day, b.loaded, b.spent, b.held = day, false, 0, 0
		b.orgs, b.payers, b.open = map[string]*laneUse{}, map[string]int64{}, map[*seat]struct{}{}
	}
	if b.loaded {
		return true
	}
	if b.store {
		kept, err := object.PaidDays(day)
		if err != nil {
			log.Error("paid lane: the day's count could not be read; nobody is seated: %v", err)
			return false
		}
		for org, n := range kept {
			b.use(org).spent += n
			b.spent += n
		}
		if err := object.DropPaidDays(utcDay(now.AddDate(0, 0, -7))); err != nil {
			log.Warn("paid lane: past days could not be dropped: %v", err)
		}
	}
	b.loaded = true
	return true
}

// write keeps n nano-dollars of org's spend on day in the store.
func (b *laneBook) write(day, org string, n int64) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	if err := object.CountPaidDay(day, org, n); err != nil {
		log.Error("paid lane: %d nano of %s's spend on %s could not be kept: %v", n, org, day, err)
	}
}

// close gives back what an open seat holds.
func (b *laneBook) close(s *seat) {
	if !s.open {
		return
	}
	s.open = false
	delete(b.open, s)
	if s.day != b.day {
		return
	}
	b.held -= s.held
	b.use(s.org).held -= s.held
	if s.payer != "" {
		if b.payers[s.payer] -= s.held; b.payers[s.payer] <= 0 {
			delete(b.payers, s.payer)
		}
	}
}

// lapse closes every seat held past seatLapse.
func (b *laneBook) lapse(now time.Time) {
	for s := range b.open {
		if now.Sub(s.at) > seatLapse {
			b.close(s)
		}
	}
}

func (b *laneBook) use(org string) *laneUse {
	if b.orgs == nil {
		b.orgs = map[string]*laneUse{}
	}
	u := b.orgs[org]
	if u == nil {
		u = &laneUse{}
		b.orgs[org] = u
	}
	return u
}

// settle gives back what the seat holds and counts n nano-dollars the call spent.
func (s *seat) settle(n int64) {
	if s != nil {
		s.book.settle(s, n, time.Now())
	}
}

// end gives back what the seat holds unless a job kept it (keep).
func (s *seat) end() {
	if s == nil {
		return
	}
	s.book.mu.Lock()
	defer s.book.mu.Unlock()
	if !s.kept {
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

// ── the estimate ────────────────────────────────────────────────────────────

// pictureTokens is what an inline image is counted as in a prompt: about what a vendor
// bills for one picture, rather than its encoded bytes.
const pictureTokens = 2000

// audioBytesPerSecond reads a recording's length from its size at 16 kbit/s, a lower
// rate than any speech codec sends, so no recording is read as shorter than it is.
const audioBytesPerSecond = 2000

// estimate is the most a call to model at c's path can cost, in nano-dollars: what
// the paid lane holds for it. A conversation is its prompt (promptTokens) and the
// completion ceiling its handler enforces (reserveCompletionTokens), for each
// provider fast mode races it to; images and video are their unit prices; speech its
// characters, a transcription its recording's length; anything else its body as a
// prompt. Tokens are priced at the most of the model's list price and its cost.
func estimate(c *zip.Ctx, model string) int64 {
	path := strings.ToLower(strings.TrimRight(c.Path(), "/"))
	body := c.Body()
	var n int64
	switch {
	case ChatPath(path):
		n = worst(model, promptTokens(body), reserveCompletionTokens(completionAsked(body)))
		if (&ApiController{Ctx: c}).wantsFast() {
			n *= fastWidth
		}
	case path == "/v1/images/generations":
		var r struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(body, &r)
		n = imageCostCents(model, max(r.N, 1)) * nanoPerCent
	case path == "/v1/videos/generations":
		n = videoCostCents(model, 1) * nanoPerCent
	case path == "/v1/audio/speech":
		var r struct {
			Input string `json:"input"`
		}
		_ = json.Unmarshal(body, &r)
		n = ttsCostNano(model, len(r.Input))
	case path == "/v1/audio/transcriptions", path == "/v1/audio/translations", strings.HasPrefix(path, transcriptPath):
		n = sttCostNano(model, float64(len(body))/audioBytesPerSecond)
	default:
		n = worst(model, promptTokens(body), 0)
	}
	return max(n, 1)
}

// worst prices prompt and completion tokens of model at the most of its list price and
// its cost.
func worst(model string, prompt, completion int) int64 {
	n := tokenCostNano(model, prompt, completion, 0, 0)
	if c := tokenProviderCostNano(model, prompt, completion, 0, 0); c != nil {
		n = max(n, *c)
	}
	return n
}

// promptTokens is what a request body may count as a prompt, in tokens: a token for
// every three bytes — a CJK character is one token in three bytes, English text one in
// four — and each inline image (a data: URL) a picture's worth.
func promptTokens(body []byte) int {
	text, pictures := len(body), 0
	for rest := body; ; {
		i := bytes.Index(rest, []byte(`"data:`))
		if i < 0 {
			break
		}
		rest = rest[i+1:]
		j := bytes.IndexByte(rest, '"')
		if j < 0 {
			j = len(rest)
		}
		text -= j
		pictures++
		rest = rest[j:]
	}
	return max(text, 0)/3 + 1 + pictures*pictureTokens
}

// completionAsked is the completion ceiling a conversation names, under any of its
// dialects' keys; 0 when it names none.
func completionAsked(body []byte) int {
	var r struct {
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
		MaxOutputTokens     int `json:"max_output_tokens"`
	}
	_ = json.Unmarshal(body, &r)
	return cmp.Or(r.MaxTokens, r.MaxCompletionTokens, r.MaxOutputTokens)
}
