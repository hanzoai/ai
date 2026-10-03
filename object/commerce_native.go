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
	// Plan says the caller's plan covered this call (LimitGrant): it debits no wallet,
	// the plan having counted it when it was admitted.
	//
	// A free call carries nothing here either. Its unit was taken at admission
	// (SpentFunc), before any model was reached, so the record of a call is money
	// alone and no count can ride on it.
	Plan bool
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
	// Ref names this DEBIT, and it is the one key a host may dedup it on. ai mints it
	// once per usage record, from nothing a caller sent, so a debit re-sent after its
	// answer was lost carries the same Ref and is charged once, while two debits —
	// two records, a hedge's loser and its winner included — never share one.
	Ref string
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

// LimitAsk names one request for a Hanzo SKU that a plan may cover: who pays, which
// member, which model and family, which app the caller signed in through, and
// whether the request may reach paid upstream.
type LimitAsk struct {
	Subject   string // the billing subject the balance gate reads
	Namespace string // the org whose ledger pays
	Actor     string // the member making the call, "<org>/<name>"
	Model     string // the model the call names
	// Family is the Hanzo family that serves the model: "enso" or "zen".
	Family string
	// Apps are the registered apps the caller's validated token was minted for (its
	// `aud`); empty for an API key. A plan covers only its consumer apps' requests.
	Apps []string
	// Spend says the request may reach its family's paid upstream within the plan's
	// budget (a chat request); without it the family answers from free models only.
	Spend bool
}

// LimitGrant is a request the caller's plan covers. The wallet is never asked about
// it: no balance gate, no reservation, no debit, no free allowance. Spend is what
// its family may spend on paid upstream for it, in nano-dollars; zero means free
// models only. Settle records, once per answer, what paid upstream actually cost in
// nano-dollars; the first settle gives back what the request held and did not use.
type LimitGrant struct {
	Plan   string
	Spend  int64
	Settle func(costNanos int64)
}

// LimitHit is why a plan refuses a request: "session" or "day" when the plan's
// request window is spent, when it resets, and the plan that raises it ("" when none
// does).
type LimitHit struct {
	Name     string
	ResetsAt time.Time
	Upgrade  string
}

// LimitFunc answers one request for a Hanzo SKU before it is served. The host
// (hanzoai/cloud) owns the plans, their windows and their budgets. A grant admits the
// request as the plan's; a hit refuses it with 429 usage_cap_exceeded naming the
// window, its reset and the upgrade; neither leaves the request to the free
// allowance; an error refuses with 503 limits_unavailable, because a limit that
// cannot be read bounds nothing. nil (standalone ai) means no plans.
type LimitFunc func(ctx context.Context, q LimitAsk) (*LimitGrant, *LimitHit, error)

// SpentFunc ADMITS one free call and answers where the caller stands after it: the
// host takes the unit when it is called, before any model is reached, and Spent says
// THIS call was past the ceiling and is refused. Window, Limit, Used and Resets
// describe the window that binds, so a refusal can say when the free lane reopens
// and an admitted call can say what is left.
//
// It is the bound on the ONE thing a wallet cannot bound. A route priced at zero
// leaves the balance gate nothing to refuse (see BalanceGateFilter), so a caller on
// the free pool is otherwise unlimited — and the free pool runs on our own compute and
// our vendors' free quotas. The allowance is that ceiling: on the Free plan, fifty
// calls per person per UTC day; call fifty-one and every one after it is refused
// until 00:00 UTC. Money and count never stand in for each other, so nothing here
// reads a balance and nothing in the wallet reads a count.
//
// THE COUNT BELONGS TO THE ADMISSION, NOT TO THE ANSWER. A call whose upstream then
// fails has still spent its unit, which is the owner's rule and the only one that
// bounds a loop retrying a failing model: counted at the answer, that loop ran free
// forever on a pool every free user shares. It also closes the overshoot an
// answer-time count had, where every call in flight read the ceiling before any of
// them was counted. A call refused at the ceiling counts nothing.
//
// THE PERSON IS NAMED BY ctx. The host keys the count by the caller's IAM home org
// and user id, which it reads with zip.CallerOf, so the gate hands it a context bound
// to the request in flight ((*zip.Ctx).Forward); subject and namespace say which
// wallet sets the ceiling. The public lane names no person: it passes the visitor as
// subject under the reserved org, and the host counts the visitor.
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
type SpentFunc func(ctx context.Context, subject, namespace string) (Standing, error)

// Standing is a caller's free-call allowance as the host counted it on admission.
//
// Window names the ceiling the numbers describe — "day" on the Free plan — and is
// the one that refused where one did. Used counts this call when it was admitted.
// Limit 0 means no window bounds the caller.
type Standing struct {
	Spent  bool
	Window string
	Limit  int64
	Used   int64
	Resets time.Time
}

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

// SetSpent installs the host's native free-call admission (nil clears it).
func SetSpent(f SpentFunc) { spent = f }

// BalanceReader returns the installed native reader, or nil when unset (standalone).
func BalanceReader() BalanceReaderFunc { return balanceReader }

// UsageRecorder returns the installed native recorder, or nil when unset.
func UsageRecorder() UsageRecorderFunc { return usageRecorder }

// TierReader returns the installed native tier reader, or nil when unset (standalone).
func TierReader() TierReaderFunc { return tierReader }

// Limits returns the installed plan AI limits, or nil when unset.
func Limits() LimitFunc { return limits }

// Spent returns the installed native free-call admission, or nil when unset.
func Spent() SpentFunc { return spent }
