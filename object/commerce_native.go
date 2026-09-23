// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package object

import (
	"context"
	"time"
)

// The ONE native billing seam. A HOST binary that embeds this module co-resident
// with the finance ledger (hanzoai/cloud, unified binary) installs these typed hooks
// at boot; every prepaid balance read + usage debit then dispatches DIRECTLY to the
// host's in-process finance client (per-org SQLite double-entry wallet) — no HTTP, no
// socket, no serialization: in-proc ZAP, not a network hop. nil (the default, e.g.
// standalone ai) → the call falls back to the module's HTTP path against
// commerceEndpoint, unchanged. This replaces the prior http.RoundTripper transport
// seam: money is a TYPED call now, never an HTTP request the host has to re-route.

// BalanceReaderFunc returns the subject's AVAILABLE prepaid balance in CENTS within
// the org namespace. subject is the billing subject ("owner/name" for a per-user
// wallet or the org slug for a pooled wallet); namespace is the org (X-Org-Id).
type BalanceReaderFunc func(ctx context.Context, subject, namespace, currency string) (availableCents int64, err error)

// UsageRecorderFunc debits a metered usage event from the subject's wallet.
type UsageRecorderFunc func(ctx context.Context, u UsageEvent) error

// UsageEvent is one metered debit — the typed twin of the old /v1/billing/usage body.
type UsageEvent struct {
	Subject   string // billing subject (SourceId): "owner/name" or org slug
	Namespace string // org (X-Org-Id)
	// USD is the EXACT amount to debit as a decimal USD string ("0.00132"), never a
	// rounded cent. The host parses it to atto-USD (1e-18) so a sub-cent AI call bills
	// precisely and is never floored to zero. Empty or "0" debits nothing.
	USD      string
	Currency string // default "usd"
	Model    string
	Provider string
	// Actor is the member who made the call, "<org>/<name>". A pooled org pays from
	// one subject; the plan's per-member limits count by this.
	Actor string
	// Allowance is the subject whose free-call allowance this call counts against. It
	// is set only when a model ANSWERED and charged nothing for doing so. Empty means
	// this call spent no allowance: it spent money, or it reached a vendor and came
	// back with an error, which is billed at what it cost and counts against no
	// ceiling.
	//
	// ONE EVENT SAYS WHAT A CALL SPENT — money, or one of a plan's free calls — so
	// counting a free call and recording that a call happened are the same act and
	// cannot come apart. recordUsage is its only producer, and it fills this field
	// only for a call a model answered, which is what makes a count impossible
	// without a model behind it.
	//
	// The allowance bounds exactly the calls a wallet cannot: the ones that cost
	// nothing. So a zero amount IS the free call, said in the currency the bound is
	// about, rather than as a second opinion about what the catalog charges.
	//
	// The subject is the payer's, except where the request named its own — the
	// public lane counts a visitor, and one shared subject would let a single caller
	// empty every visitor's day.
	Allowance string
	// RequestID names the metered call so a warehouse row, a span and a support
	// question can be tied back to it.
	//
	// IT IS NOT A DEDUP KEY, and nothing downstream reads it as one. The host that
	// owns the ledger mints each entry's own id server-side and drops this field at
	// the seam — deliberately, because a caller who could pick the ledger's key
	// could be billed once for every completion after the first. So a debit sent
	// twice under one RequestID is charged twice. A caller that must not double
	// charge has to make the call once itself; there is no dedup to fall back on.
	RequestID string
}

// TierReaderFunc returns the subject's commerce subscription-plan NAME
// (free | starter | pro | enterprise) within the org namespace — the co-resident
// twin of the family per-tier gate's HTTP lookup. subject is the billing subject
// ("owner/name" or the org slug); namespace is the org (X-Org-Id). A HOST binary
// (hanzoai/cloud) installs it so the tier is read through the in-process commerce
// transport with the service token commerce accepts — NOT an authed self-call to
// the cloud edge, which the edge would 401/403 (the toothless-gate bug: the gate
// then saw "" and failed open). nil (the default, e.g. standalone ai) → the tier
// lookup falls back to the module's HTTP path, unchanged. A "" name with a nil error
// means the tier is UNKNOWN, which the gate treats as ALLOW (fail-safe), so a
// commerce blip never locks a paying caller out of a SKU they already had.
type TierReaderFunc func(ctx context.Context, subject, namespace string) (name string, err error)

// LimitAsk names the priced call a plan's AI limits are asked about.
type LimitAsk struct {
	Subject   string // the billing subject the balance gate reads
	Namespace string // the org whose ledger pays
	Actor     string // the member making the call, "<org>/<name>"
	Model     string // the model the call names
}

// LimitHit is why a priced call is refused: a plan window that is full (session,
// weekly, weekly_premium, month) and when it resets, or "plan" when no plan covers
// AI and no bought credit pays, which carries no reset.
type LimitHit struct {
	Name     string
	ResetsAt time.Time
}

// LimitFunc answers whether a priced call fits inside the caller's plan AI limits.
// The host (hanzoai/cloud) owns the plans, the windows and the usage they sum. A nil
// hit leaves the call to the balance gate; a full window refuses with 429
// usage_cap_exceeded naming it and its reset; "plan" refuses with 402 plan_required
// and the upgrade link; an error refuses with 503 limits_unavailable, because a
// limit that cannot be read bounds nothing. It is asked only for priced models, so a free model is
// never refused by it. nil (standalone ai) means no plan limits.
type LimitFunc func(ctx context.Context, q LimitAsk) (*LimitHit, error)

// SpentFunc reports whether subject has already used the free calls its plan allows
// this period, within the org namespace.
//
// It is the bound on the ONE thing a wallet cannot bound. A route priced at zero
// leaves the balance gate nothing to refuse (see BalanceGateFilter), so a caller on
// the free pool is otherwise unlimited — and the free pool runs on our own compute.
// The allowance is that ceiling: a COUNT of calls, per subject, per period, from the
// caller's plan. Money and count never stand in for each other, so nothing here reads
// a balance and nothing in the wallet reads a count.
//
// IT ONLY READS, and that is the whole shape of the thing. A ceiling on SPEND is
// reached when a model is reached, so the count belongs to the answer and not to the
// attempt: it rides on UsageEvent.Allowance, a field on the record of a call that
// served. Asking here therefore costs the caller nothing, a subject at the ceiling
// keeps their count where it is, and a request that dies before any model — an
// unresolvable route, a vendor that never answered, a deployment mid-roll — leaves
// the caller exactly as many free calls as it found.
//
// THE TRADE IS DELIBERATE, AND IT IS NOT A SMALL ONE. Every call a subject has in
// flight reads the ceiling before any of them has been counted, so all of them are
// admitted: the overshoot is the subject's CONCURRENCY, not one or two. What bounds it
// is the edge's per-IP flood cap ahead of this, and how long an answer takes — not
// this read. Undershoot is the error going the other way, and it is the one a caller
// feels while we never see it: a day spent on a route that 404'd looks, from here,
// exactly like a day spent on answers. The direction of the error is chosen rather
// than left to chance, and the size of it is a number to watch.
//
// AN ERROR IS ALLOWED THROUGH, and WHO gets that benefit is the host's call, not
// this module's. A route stated at zero is not always served by our own compute — a
// vendor can be behind it and bills us either way — so an unanswerable allowance is
// not automatically safe. The host holds the tenancy vocabulary, so it answers
// spent=true for a caller it cannot name and returns the error only for one it can,
// where a blip must not take the free models from a paying customer.
//
// The hard money bounds are untouched: a priced route never reaches this branch. A
// plan with no limit configured, and any caller whose plan is unlimited, is never
// spent. nil (the default, standalone ai) → no allowance, behavior unchanged.
type SpentFunc func(ctx context.Context, subject, namespace string) (spent bool, err error)

var (
	balanceReader BalanceReaderFunc
	usageRecorder UsageRecorderFunc
	tierReader    TierReaderFunc
	limits        LimitFunc
	spent         SpentFunc
)

// SetBalanceReader installs the host's native balance reader (nil clears it).
func SetBalanceReader(f BalanceReaderFunc) { balanceReader = f }

// SetUsageRecorder installs the host's native usage recorder (nil clears it).
func SetUsageRecorder(f UsageRecorderFunc) { usageRecorder = f }

// SetTierReader installs the host's native subscription-tier reader (nil clears it).
func SetTierReader(f TierReaderFunc) { tierReader = f }

// SetLimits installs the host's plan AI limits (nil clears them).
func SetLimits(f LimitFunc) { limits = f }

// SetSpent installs the host's native plan-allowance read (nil clears it).
func SetSpent(f SpentFunc) { spent = f }

// BalanceReader returns the installed native reader, or nil when unset (standalone).
func BalanceReader() BalanceReaderFunc { return balanceReader }

// UsageRecorder returns the installed native recorder, or nil when unset.
func UsageRecorder() UsageRecorderFunc { return usageRecorder }

// TierReader returns the installed native tier reader, or nil when unset (standalone).
func TierReader() TierReaderFunc { return tierReader }

// Limits returns the installed plan AI limits, or nil when unset.
func Limits() LimitFunc { return limits }

// Spent returns the installed native allowance read, or nil when unset.
func Spent() SpentFunc { return spent }
