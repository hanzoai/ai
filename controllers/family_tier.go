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

// family_tier.go — per-tier access gating for family SKUs.
//
// A family SKU advertises a min_tier on the enso subscription ladder
// (free < trial < paid): enso-flash is free (everyone), enso is trial+, enso-ultra is
// paid-only. ai learns this from discovery (zenModel.minTier), never a hardcode. The
// gate asks commerce for the caller's real plan (GET /v1/billing/tier), maps it onto
// the ladder, and admits the SKU only when the caller's rung is at least its min_tier.
//
// FAIL-SAFE is the whole design: this gate only ADDS a restriction to new tier-gated
// SKUs, so any uncertainty ALLOWS. A free/unset min_tier, an empty subject, a model
// unknown to discovery, or a commerce lookup that cannot confidently name the tier
// (unconfigured, transport error, non-2xx, decode, or timeout) all admit the request.
// Only a CONFIDENT commerce tier strictly below the SKU's min_tier denies — a transient
// commerce blip must never lock a paying caller out of a SKU they already had.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
)

// ── the enso ladder (values, not places) ─────────────────────────────────────

// ensoTierRank ranks an enso-ladder tier for comparison: free(0) < trial(1) < paid(2).
// "" and any unrecognized value fold to free — the least-privileged rung — so a SKU
// with no min_tier is open to all and an unknown tier is treated conservatively.
func ensoTierRank(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "paid":
		return 2
	case "trial":
		return 1
	default: // "free", ""
		return 0
	}
}

// commerceTierToLadder maps a commerce plan NAME — the tier.name in GET /v1/billing/tier
// (free | starter | pro | enterprise) — onto the enso ladder: free→free, starter→trial,
// pro/enterprise→paid. It is only ever called with a CONFIDENT (non-empty) name; an
// unrecognized name folds to free (reached only when commerce answered, so never a blip).
func commerceTierToLadder(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "starter":
		return "trial"
	case "pro", "enterprise":
		return "paid"
	default: // "free", "developer", legacy "zen-free", …
		return "free"
	}
}

// familyTierAllowed reports whether the caller's commerce tier qualifies for a family
// SKU's advertised min_tier. See the file header for the fail-safe contract: only a
// CONFIDENT commerce tier strictly below the SKU's min_tier denies; everything else
// admits (free SKU, unknown SKU, empty subject, or an unresolvable commerce tier).
func familyTierAllowed(subject, model string) bool {
	zm, ok := familyLookupFresh(model)
	if !ok {
		return true // unknown to discovery → not tier-gated (mirrors FamilyModelGated's fail-open)
	}
	need := ensoTierRank(zm.minTier())
	if need == 0 {
		return true // free SKU → every tier (and the unauthenticated direct path) allowed
	}
	name := familyTier(subject)
	if strings.TrimSpace(name) == "" {
		return true // commerce could not confidently name the tier → fail safe (allow)
	}
	return ensoTierRank(commerceTierToLadder(name)) >= need
}

// ── subject resolution (the SAME payer the balance gate bills) ────────────────

// familyAccessSubject resolves the commerce billing subject for the tier gate the way
// enforceBalanceGate does — account.Payer over the caller's own credential — so the tier
// is read for the identical subject the balance read and usage debit key on. Falls back
// to the org context when there is no authUser; "" when neither names an owner (→
// familyTier returns "" → the gate fails safe to allow).
func familyAccessSubject(orgId string, authUser *iam.User) string {
	if authUser != nil {
		return account.Payer(account.Credential{
			Owner:   authUser.Owner,
			Name:    authUser.Name,
			Machine: account.IsMachine(authUser.Type),
		}).Subject()
	}
	return subjectFromOrg(orgId)
}

// subjectFromOrg derives the commerce subject from an org-only context (the auto-router's
// `known` predicate holds just the orgId). It funnels through the SAME account.Payer rule
// so an org resolves to the identical subject the direct path keys on. Empty orgId → ""
// (no subject → the tier lookup fails safe to allow).
func subjectFromOrg(orgId string) string {
	return account.Payer(account.Credential{Owner: orgId}).Subject()
}

// ── the cached commerce tier client (IO edge) ────────────────────────────────

// familyTierTTL is how long a resolved commerce tier is trusted before re-fetch — short
// enough that a plan change takes effect within a minute, long enough to keep the gate
// off the per-request hot path (a warm subject is a map read).
const familyTierTTL = 60 * time.Second

// familyTierHTTPTimeout bounds the commerce tier lookup so a slow/hung commerce never
// stalls the gate; on timeout the lookup returns "" (unknown → allow).
const familyTierHTTPTimeout = 5 * time.Second

type familyTierCacheEntry struct {
	name string
	at   time.Time
}

var (
	familyTierMu    sync.Mutex
	familyTierCache = map[string]familyTierCacheEntry{}
	familyTierHTTP  = &http.Client{Timeout: familyTierHTTPTimeout}
)

// familyTier resolves the caller's commerce billing-tier NAME (free|starter|pro|
// enterprise), cached ~60s per subject. It returns "" when commerce cannot confidently
// answer (unconfigured, transport error, non-2xx, decode, or timeout); "" means unknown,
// which the tier gate treats as ALLOW (fail-safe). Indirected through a var so tests
// inject a tier without a live commerce (like orgAutoRoutingLookup / routingEventSink).
var familyTier = cachedFamilyTier

// cachedFamilyTier is familyTier's production implementation: a per-subject 60s cache in
// front of the commerce lookup. A cached value (including "") is trusted for the TTL, so
// a commerce outage neither hammers commerce nor flaps the decision — it holds the last
// answer, and an unknown ("") holds as allow.
func cachedFamilyTier(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "" // no subject to key on → unknown → allow
	}
	now := time.Now()

	familyTierMu.Lock()
	if e, ok := familyTierCache[subject]; ok && now.Sub(e.at) < familyTierTTL {
		familyTierMu.Unlock()
		return e.name
	}
	familyTierMu.Unlock()

	name := commerceFamilyTier(subject)
	if strings.TrimSpace(name) == "" {
		noteTierUnknown(subject)
	}

	familyTierMu.Lock()
	familyTierCache[subject] = familyTierCacheEntry{name: name, at: now}
	familyTierMu.Unlock()
	return name
}

// tierUnknownLogEvery throttles the unknown-tier warning to one line per interval across
// all subjects — enough to see a broken tier read, never enough to flood a busy gateway.
const tierUnknownLogEvery = time.Minute

var (
	tierUnknownMu   sync.Mutex
	tierUnknownAt   time.Time
	tierUnknownSeen int
)

// noteTierUnknown records that commerce could not name a caller's tier. An unknown tier
// is this gate's fail-safe ALLOW and the funding gate's fail-CLOSED refusal, so a tier
// read that is quietly broken disables the enso ladder and the free-tier routing cap
// while refusing every prepaid SKU — with no symptom anywhere. That is exactly how the
// nil-org 500 on GET /v1/billing/tier went unnoticed: the gate swallowed every error and
// said nothing. A silent gate is an unverifiable gate, so it says something now.
func noteTierUnknown(subject string) {
	tierUnknownMu.Lock()
	tierUnknownSeen++
	if since := time.Since(tierUnknownAt); since < tierUnknownLogEvery && !tierUnknownAt.IsZero() {
		tierUnknownMu.Unlock()
		return
	}
	n := tierUnknownSeen
	tierUnknownSeen = 0
	tierUnknownAt = time.Now()
	tierUnknownMu.Unlock()
	log.Warning("family_tier: commerce could not name the tier for %q (%d unknown reads since the last line) — the SKU ladder and the free-tier routing cap ADMIT on an unknown tier, and the prepaid funding gate REFUSES on it", subject, n)
}

// commerceFamilyTier performs the un-cached lookup of the plan NAME at tier.name.
//
// Native path FIRST: a co-resident host (cloud) installs object.TierReader so the tier
// is read DIRECTLY through the in-process commerce transport — no HTTP to the cloud
// edge. This is the fix for the toothless gate: the edge 401/403s a service self-call
// to /v1/billing/*, so the plain-HTTP path below always returned "" in-cluster and the
// gate failed OPEN. The native seam mirrors getUserBalance's object.BalanceReader():
// cloud owns the co-resident read (over commerceinproc, with the service token commerce
// itself accepts), ai stays transport-agnostic. Any reader error → "" (unknown → allow).
//
// Standalone fallback: authed HTTP GET {commerceEndpoint}/v1/billing/tier?user=<subject>
// (the same commerceEndpoint / commerceToken conf keys and X-Org-Id namespace scoping as
// getUserBalance's HTTP fallback). Returns "" on ANY failure so the gate fails safe (allow).
func commerceFamilyTier(subject string) string {
	namespace := tierNamespace(subject)

	if r := object.TierReader(); r != nil {
		name, err := r(context.Background(), subject, namespace)
		if err != nil {
			return "" // co-resident commerce hiccup → unknown → allow (fail-safe)
		}
		return strings.TrimSpace(name)
	}

	endpoint := strings.TrimRight(conf.GetConfigString("commerceEndpoint"), "/")
	if endpoint == "" {
		return "" // commerce not configured → unknown → allow
	}
	reqURL := fmt.Sprintf("%s/v1/billing/tier?user=%s", endpoint, url.QueryEscape(subject))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return ""
	}
	if token := conf.GetConfigString("commerceToken"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Scope the service-token call to the caller's namespace (the org slug — the subject
	// itself for an org account, or its "<org>/" prefix for a person), matching
	// getUserBalance's X-Org-Id stamping so the tier is read from the right tenant.
	req.Header.Set("X-Org-Id", namespace)

	resp, err := familyTierHTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	// Same shape as ratelimit.commerceTierResponse: the plan name lives at tier.name.
	var out struct {
		Tier struct {
			Name string `json:"name"`
		} `json:"tier"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ""
	}
	return strings.TrimSpace(out.Tier.Name)
}

// tierNamespace is the org slug a subject lives under: the subject itself for an org
// account (the bare slug), or the "<org>/" prefix for a person/project subject — the
// same org-from-key rule account.PayerOf uses.
func tierNamespace(subject string) string {
	if i := strings.IndexByte(subject, '/'); i >= 0 {
		return subject[:i]
	}
	return subject
}

// ── the funding gate: solvency, not upsell ───────────────────────────────────

// familyFundingAllowed reports whether the caller may spend a PREPAID (real-cash)
// upstream. It is the mirror image of familyTierAllowed above, and deliberately so.
//
// The two gates answer different questions and therefore fail in opposite directions:
//
//	familyTierAllowed  — "does this caller's PLAN include this SKU?" A commercial
//	    upsell. Being wrong costs one free taste, so uncertainty ADMITS: a commerce
//	    blip must never lock out a caller who already had the SKU.
//	familyFundingAllowed — "may this caller spend our CASH?" Solvency. Being wrong
//	    spends real money on someone who is not paying us, and a prepaid balance that
//	    hits zero takes the SKU down for every paying customer. So uncertainty REFUSES.
//
// A gate that opens on a timeout is not a gate on money. These were one fused value
// (min_tier) until the family began advertising `funding` separately, which is what let
// the funding floor silently inherit the upsell floor's fail-open behavior.
//
// Credit-funded SKUs (DigitalOcean, AWS grants) are unaffected: they carry no funding
// class, so this gate admits them and only the subscription floor applies.
func familyFundingAllowed(subject, model string) bool {
	if _, served := familyServing(model); !served {
		// Not a family SKU at all. ai's own providers carry their own commercial terms;
		// this gate speaks only for the model families, so it has no opinion here.
		return true
	}
	zm, ok := familyLookupFresh(model)
	if !ok {
		// A family claims this id BY PREFIX, but discovery cannot describe it — so we
		// cannot say what it costs us to serve. The subscription gate treats undescribed
		// as "not gated" and admits; a funding gate must refuse, because the prefix router
		// will still route it to the family and something will pay for it.
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(zm.Funding), "prepaid") {
		return true // credit-funded — the subscription floor is the only gate that applies
	}
	// Prepaid from here on: the caller must be a CONFIRMED paying subscriber.
	name := familyTier(subject)
	if strings.TrimSpace(name) == "" {
		return false // commerce could not name the tier → refuse to spend cash on a guess
	}
	return ensoTierRank(commerceTierToLadder(name)) >= ensoTierRank("paid")
}
