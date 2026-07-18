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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/iam"
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

	familyTierMu.Lock()
	familyTierCache[subject] = familyTierCacheEntry{name: name, at: now}
	familyTierMu.Unlock()
	return name
}

// commerceFamilyTier performs the un-cached lookup: GET
// {commerceEndpoint}/v1/billing/tier?user=<subject>, returning the plan NAME at
// tier.name. It mirrors getUserBalance's URL/subject/header pattern (the same
// commerceEndpoint / commerceToken conf keys and X-Org-Id namespace scoping) and
// ratelimit's commerceTierLookup response shape. Returns "" on ANY failure so the gate
// fails safe (allow).
func commerceFamilyTier(subject string) string {
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
	req.Header.Set("X-Org-Id", tierNamespace(subject))

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
