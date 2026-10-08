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

// lane.go — which lane a request is served on.
//
// FreeOnly is the platform's switch: off, no request reaches a priced route. On, each
// request is served on one of two lanes, decided once, from the grant the host's usage
// policy left on it (Lane):
//
//   - PAID: a paid plan stands behind the payer (LimitGrant.Plan — an active
//     subscription whose period was paid) and something pays for this priced call:
//     the plan's included usage first (PaysPlan), then the payer's prepaid or granted
//     credit (PaysPrepaid, PaysCredits). How much a plan includes is the plan's own
//     figure, held by the host per plan and per seat, so Max 20x reaches the paid lane
//     for the most usage, Pro for less, and a plan that includes none never does.
//   - FREE: everyone else — no plan (a free signup, a wallet with no plan, a guest, a
//     key nobody can name), a call nothing pays for, a free model, and any request that
//     reached a handler without passing the filter that decides (a ZAP twin, a
//     sibling's completion over the plane). A payer whose plan has used what it
//     includes is handed to the free model in limited mode before it gets here
//     (routers' fallback), so the free lane answers them rather than an error.
//
// Above every plan sits one platform-wide guard, PAID_LANE_DAILY: what all paid-lane
// calls together may spend in a UTC day. Past it every request is on the free lane.
//
// Every read of the switch on the request path asks FreeOnlyFor, never FreeOnly.
//
// The response says which: X-Hanzo-Lane is paid or free, and X-Hanzo-Lane-Reason is
// paid_lane_ceiling when a payer is on the free lane because the platform's paid-lane
// day is spent; a chat request for a model only the paid lane serves is then answered
// by the free model in limited mode (routers' BalanceGateFilter). Who pays is the
// grant's, unchanged by the lane (X-Hanzo-Paid-By).

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

const (
	// LaneHeader names the lane the request was served on: paid or free.
	LaneHeader = "X-Hanzo-Lane"
	// LaneReasonHeader says why a payer is on the free lane: ReasonCeiling.
	LaneReasonHeader = "X-Hanzo-Lane-Reason"
	// ReasonCeiling is why a payer is on the free lane when the platform's paid-lane day
	// is spent.
	ReasonCeiling = "paid_lane_ceiling"
)

// laneKey is where a request carries whether it is on the paid lane.
type laneKey struct{}

// Lane decides the lane of the request on c and leaves it on the request's context,
// which its handlers and their usage records read.
func Lane(c *zip.Ctx) {
	g := grantFrom(c.Context())
	ceiling := Ceiling(g)
	paid := !FreeOnly() && planPays(g) && !ceiling
	c.SetContext(context.WithValue(c.Context(), laneKey{}, paid))
	if paid {
		c.SetHeader(LaneHeader, "paid")
	} else {
		c.SetHeader(LaneHeader, "free")
	}
	if ceiling {
		c.SetHeader(LaneReasonHeader, ReasonCeiling)
	}
}

// Ceiling reports whether a grant's request would be on the paid lane but for the
// platform's paid-lane day being spent.
func Ceiling(g *object.LimitGrant) bool {
	return !FreeOnly() && planPays(g) && !paidDay.left(utcDay(time.Now()), paidLaneDaily())
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

// paidLane reports whether the request on ctx was put on the paid lane.
func paidLane(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	paid, _ := ctx.Value(laneKey{}).(bool)
	return paid
}

// onPaidLane is paidLane, indirected so this package's tests, which exercise the paid
// lane, can put every request on it.
var onPaidLane = paidLane

// FreeOnlyFor reports whether the paid lane is closed to the request on ctx: the
// platform's switch is off, or the request was not put on the paid lane.
func FreeOnlyFor(ctx context.Context) bool {
	return FreeOnly() || !onPaidLane(ctx)
}

// paidLaneDefault is the platform's paid-lane day when PAID_LANE_DAILY is unset, in
// nano-dollars: $10.
const paidLaneDefault = 10 * 1_000_000_000

// paidLaneDaily is what every paid-lane call together may spend in a UTC day, in
// nano-dollars. PAID_LANE_DAILY states it in USD; 0 closes the paid lane, and a value
// that does not read as a positive amount closes it too.
func paidLaneDaily() int64 {
	s := strings.TrimSpace(conf.GetConfigString("PAID_LANE_DAILY"))
	if s == "" {
		return paidLaneDefault
	}
	usd, err := strconv.ParseFloat(s, 64)
	if err != nil || !(usd > 0) {
		return 0
	}
	return usdToNano(usd)
}

// laneDay is one UTC day of paid-lane spend, in nano-dollars. When the day turns the
// count starts again at zero.
//
// It is this process's count. ai runs as one replica (bootstrap's single-pod
// invariant), so it is the platform's; a restart starts the day again.
//
// Calls in flight all read the day before any of them is counted, so the day can pass
// its ceiling by what those calls cost.
type laneDay struct {
	mu    sync.Mutex
	day   string
	spent int64
}

var paidDay = &laneDay{}

// left reports whether the paid lane has spend left on day under ceiling.
func (d *laneDay) left(day string, ceiling int64) bool {
	if ceiling <= 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.day != day || d.spent < ceiling
}

// add counts nanos of paid-lane spend against day.
func (d *laneDay) add(day string, nanos int64) {
	if nanos <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.day != day {
		d.day, d.spent = day, 0
	}
	d.spent += nanos
}

// laneSpend is what a call on the paid lane counts against the day, in nano-dollars:
// the most of its price, its cost and what it spent of a plan, so a model sold below
// its cost, or at zero on a plan, counts what it cost.
func laneSpend(r *usageRecord) int64 {
	m := usageMargin(r)
	n := max(m.BilledNano, r.planNanos)
	if m.CostNano != nil {
		n = max(n, *m.CostNano)
	}
	return n
}
