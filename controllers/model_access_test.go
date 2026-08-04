// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// A family SKU that advertises access:"waitlist" is parsed as gated.
func TestZenModelGatedParse(t *testing.T) {
	gated := zenWireModel{ID: "enso", Access: "waitlist"}.model()
	if !gated.gated() {
		t.Fatal("enso (access=waitlist) should parse as gated")
	}
	open := zenWireModel{ID: "zen5"}.model()
	if open.gated() {
		t.Fatal("zen5 (no access) should not be gated")
	}
}

// familyRefusalFor is the enforcement decision AND its reason: a non-gated model is
// always allowed; a gated model with no grant (no DB / no row) is refused as a waitlist,
// closed by default.
func TestFamilyRefusalForDecision(t *testing.T) {
	fam := &modelFamily{name: "enso", prefix: "enso"}
	fam.byID = map[string]zenModel{
		"enso": {ID: "enso", Access: "waitlist"},
		"open": {ID: "open"},
	}
	c := &ApiController{}

	// Non-gated model → allowed regardless of caller.
	if r := c.familyRefusalFor(fam, "open", "acme", nil); r != refusalNone {
		t.Fatalf("non-gated model must be allowed, got refusal %q", r)
	}
	// Model unknown to discovery → treated as non-gated (allowed).
	if r := c.familyRefusalFor(fam, "mystery", "acme", nil); r != refusalNone {
		t.Fatalf("model unknown to discovery must be allowed (not gated), got refusal %q", r)
	}
	// Gated model, no grant row (no DB in test) → refused, AS A WAITLIST: the remedy is
	// to request access, and only this reason may say so.
	if r := c.familyRefusalFor(fam, "enso", "acme", &iam.User{Owner: "acme", Name: "bob"}); r != refusalWaitlist {
		t.Fatalf("gated model without a grant must be refused as %q, got %q", refusalWaitlist, r)
	}
}

// A refusal names the remedy that actually clears IT. The waitlist refusal tells the
// caller how to request access; the PLAN refusal must not — a caller whose subscription
// does not include the SKU can request access forever and never be unblocked, which is
// how enso-ultra (min_tier "paid", no waitlist at all) answered an unsubscribed caller
// on api.hanzo.ai. It points at the plan page instead, and never at the wallet: credits
// cannot buy a rung on the ladder.
func TestFamilyRefusalMessageNamesItsOwnRemedy(t *testing.T) {
	wait := refusalWaitlist.message("enso")
	if !strings.Contains(wait, "limited preview") || !strings.Contains(wait, "POST /v1/models/enso/access") {
		t.Fatalf("waitlist refusal missing the request-access instruction: %q", wait)
	}

	plan := refusalPlan.message("enso-ultra")
	if strings.Contains(plan, "limited preview") || strings.Contains(plan, "/access") {
		t.Fatalf("plan refusal must NOT send the caller to the waitlist — that remedy can never clear it: %q", plan)
	}
	if !strings.Contains(plan, "enso-ultra") || !strings.Contains(plan, "plan") {
		t.Fatalf("plan refusal must name the model and the plan: %q", plan)
	}
	if !strings.Contains(plan, "console.hanzo.ai/billing/subscriptions") {
		t.Fatalf("plan refusal must carry the page where a plan is actually changed: %q", plan)
	}
	if strings.Contains(plan, "pay.hanzo.ai") {
		t.Fatalf("plan refusal must not point at the wallet — credits cannot clear it: %q", plan)
	}
}

// FamilyModelGated keys off discovery: a gated SKU is recognized as access-gated.
// Enso is PAID — gating controls ACCESS (a grant unlocks the SKU), never billing;
// there is no comp path, so a granted caller still pays the normal balance gate.
func TestFamilyModelGatedDecision(t *testing.T) {
	// Seed the enso family's discovered catalog with a gated SKU.
	saved := ensoFam.byID
	ensoFam.byID = map[string]zenModel{"enso": {ID: "enso", Access: "waitlist"}}
	defer func() { ensoFam.byID = saved }()

	if !FamilyModelGated("enso") {
		t.Fatal("enso should be recognized as gated")
	}
	if FamilyModelGated("zen5") {
		t.Fatal("zen5 is not gated")
	}
}

// mergeModels stamps a gated SKU with the default "waitlist" standing so /v1/models
// advertises the gating to everyone (the caller's real status is overlaid later).
func TestMergeModelsGatedAnnotation(t *testing.T) {
	fam := &modelFamily{name: "enso", prefix: "enso"}
	fam.byID = map[string]zenModel{
		"enso": {ID: "enso", Access: "waitlist", Base: zenTier{}},
	}
	fam.ids = []string{"enso"}
	fam.loaded = true // enabled() is false (no urlKey) so snapshot() uses this data directly

	out := fam.mergeModels(nil)
	if len(out) != 1 {
		t.Fatalf("mergeModels returned %d models, want 1", len(out))
	}
	if out[0].Access == nil || out[0].Access.State != "waitlist" {
		t.Fatalf("gated SKU access = %+v, want state=waitlist", out[0].Access)
	}
}

// annotateModelAccess is a no-op without an authenticated caller (nothing to overlay).
func TestAnnotateModelAccessNilUser(t *testing.T) {
	models := []modelInfo{{ID: "enso", Access: &modelAccessInfo{State: "waitlist"}}}
	annotateModelAccess(models, nil)
	if models[0].Access.State != "waitlist" {
		t.Fatal("annotate with nil user must not change the default standing")
	}
}
