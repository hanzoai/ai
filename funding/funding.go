// Copyright 2026 Hanzo AI, Inc. All rights reserved.

// Package funding is the cash circuit-breaker. It is deliberately NOT under
// internal/: the HOST publishes into it (hanzoai/cloud computes upstream credit
// and burn, ai enforces the ceiling), so the seam has to cross the module
// boundary. Policy lives here with the enforcement it guards; the numbers arrive
// from whoever can see the vendor.
//
// Package funding is the cash circuit-breaker: the one place that answers
// "may this request spend REAL money right now, and how much is left today".
//
// WHY IT EXISTS. There are two separate moneys and, until this package, nothing
// connecting them. The commerce ledger is platform credit we ISSUE — accounting.
// The upstream provider bill is cash LEAVING THE BANK. On 2026-07-28 Hanzo had
// $250,573 of outstanding platform credit standing against $0.00 of remaining
// DigitalOcean promotional credit and a ~$65/day cash burn: every one of those
// issued dollars could convert 1:1 into cash, and no layer could even notice,
// because each was correctly answering its own question. The promo credit had in
// fact been exhausted for six weeks; invoices went $0.00 → $0.53 (June) →
// $1,824.45 (July) while the dashboard still showed a hardcoded grant.
//
// WHY A CEILING AND NOT A REFUSAL. The obvious rule — "comp credit may not spend
// a paid provider" — is degenerate the moment the promo runs out: with credit at
// zero EVERY provider is paid-only, so that rule refuses every comp-funded
// request at once, including live customer demos. It converts a budget problem
// into an outage. A daily ceiling bounds the loss without the cliff, which is the
// actual ask: know what is left, and keep it filled.
//
// DEFAULT OFF. A zero ceiling disables the breaker, so merging this changes no
// behaviour anywhere until someone deliberately arms it. A money guard that
// switches itself on during a deploy is its own incident.
//
// FAIL OPEN, DELIBERATELY, AND ONLY HERE. This is a SECONDARY guard; the prepaid
// balance gate is the primary one and stays fail-closed (an unverifiable balance
// denies). If this breaker's own inputs are unknown — cloud has not pushed state,
// or the day rolled over — it ALLOWS. Fail-closed here would mean a stale
// telemetry push could halt all paid inference fleet-wide, which is a worse
// failure than one extra day of spend against a ceiling someone is already
// watching.
package funding

import (
	"sync/atomic"
)

// State is what cloud knows about upstream funding, pushed in. It is a VALUE:
// the breaker never reads a clock, a ledger or a vendor API, so the decision is a
// pure function of what it was told and is trivially testable.
type State struct {
	// OnCash reports that upstream promotional credit is exhausted, so spend is
	// now real money. False while a grant still covers usage.
	OnCash bool
	// TodayCents is cash spent so far in the current UTC day.
	TodayCents int64
	// CeilingCents caps cash per UTC day. Zero (or negative) DISABLES the breaker.
	CeilingCents int64
}

// Refuse reports whether this request must be turned away to protect the bank.
//
// It refuses only when all three are true: the ceiling is armed, upstream credit
// is gone, and today's cash has already reached the ceiling. Any unknown leaves
// the request allowed — see the fail-open note above.
func (s State) Refuse() bool {
	if s.CeilingCents <= 0 || !s.OnCash {
		return false
	}
	return s.TodayCents >= s.CeilingCents
}

// RemainingCents is today's unspent cash allowance, never negative. It is the
// number worth putting in an error message: it tells an operator how much room is
// left rather than only that they ran out.
func (s State) RemainingCents() int64 {
	if s.CeilingCents <= 0 {
		return 0
	}
	if r := s.CeilingCents - s.TodayCents; r > 0 {
		return r
	}
	return 0
}

// current holds the pushed State. atomic so Publish (a background refresh) and
// Current (every request) never contend on the hot path.
var current atomic.Pointer[State]

// Publish replaces the funding state. cloud calls this from the same finance
// aggregation that renders the provider-credit board, so the breaker and the
// dashboard can never disagree about whether we are on cash. Passing a zero State
// disarms the breaker.
func Publish(s State) { current.Store(&s) }

// Current returns the last published State, or the zero State (breaker disarmed)
// when cloud has never pushed one — the standalone-ai case, and the reason a
// missing publisher cannot halt inference.
func Current() State {
	if p := current.Load(); p != nil {
		return *p
	}
	return State{}
}
