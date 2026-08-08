// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
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

// familyAccessAllowed is the enforcement decision: a non-gated model is always
// allowed; a gated model with no grant (no DB / no row) is denied — closed by default.
func TestFamilyAccessAllowedDecision(t *testing.T) {
	fam := &modelFamily{name: "enso", prefix: "enso"}
	fam.byID = map[string]zenModel{
		"enso": {ID: "enso", Access: "waitlist"},
		"open": {ID: "open"},
	}
	c := &ApiController{}

	// Non-gated model → allowed regardless of caller.
	if !c.familyAccessAllowed(fam, "open", "acme", nil) {
		t.Fatal("non-gated model must be allowed")
	}
	// Model unknown to discovery → treated as non-gated (allowed).
	if !c.familyAccessAllowed(fam, "mystery", "acme", nil) {
		t.Fatal("model unknown to discovery must be allowed (not gated)")
	}
	// Gated model, no grant row (no DB in test) → denied.
	if c.familyAccessAllowed(fam, "enso", "acme", &iam.User{Owner: "acme", Name: "bob"}) {
		t.Fatal("gated model without a grant must be denied")
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

// The gated denial message tells the caller exactly how to request access.
func TestGatedAccessMessage(t *testing.T) {
	msg := gatedAccessMessage("enso")
	if !strings.Contains(msg, "limited preview") || !strings.Contains(msg, "POST /v1/models/enso/access") {
		t.Fatalf("gated message missing the request-access instruction: %q", msg)
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

// Money buys access, so plan and prepaid tier no longer gate the family pipe:
// a funded caller (enforceBalanceGate, every serve path) may use any model, and
// a plan only meters USAGE. The one refusal familyRefusal still names is the
// preview waitlist — access to an unreleased SKU, not a paywall.
func TestFamilyRefusalNamesTheGate(t *testing.T) {
	catalog := map[string]zenModel{
		"enso":      {ID: "enso", MinTier: "trial"},
		"enso-pre":  {ID: "enso-pre", Funding: "prepaid"},
		"enso-prev": {ID: "enso-prev", Access: "waitlist"},
	}
	fam := &modelFamily{name: "enso", prefix: "enso"}
	fam.byID = catalog
	savedByID := ensoFam.byID
	ensoFam.byID = catalog
	t.Cleanup(func() { ensoFam.byID = savedByID })

	savedProv := ensoFam.providerFn
	ensoFam.providerFn = func() *object.Provider { return &object.Provider{ProviderUrl: "http://enso.test"} }
	t.Cleanup(func() { ensoFam.providerFn = savedProv })

	savedTier := familyTier
	familyTier = func(string) string { return "free" }
	t.Cleanup(func() { familyTier = savedTier })

	c := &ApiController{}

	// A plan below the SKU's old min_tier no longer refuses — money is the gate,
	// and it lives on the balance path, not here.
	if msg := c.familyRefusal(fam, "enso", "acme", nil); msg != "" {
		t.Fatalf("tier refusal = %q, want no refusal (money buys access)", msg)
	}

	// Prepaid capacity is likewise not plan-gated at the family pipe.
	if msg := c.familyRefusal(fam, "enso-pre", "acme", nil); msg != "" {
		t.Fatalf("funding refusal = %q, want no refusal (money buys access)", msg)
	}

	// A preview SKU without a grant → the request-access sentence, unchanged.
	msg := c.familyRefusal(fam, "enso-prev", "acme", &iam.User{Owner: "acme", Name: "bob"})
	if !strings.Contains(msg, "limited preview") || !strings.Contains(msg, "POST /v1/models/enso-prev/access") {
		t.Fatalf("grant refusal = %q, want the request-access sentence", msg)
	}

	// A servable SKU refuses nothing: paid caller, no gate in the way.
	familyTier = func(string) string { return "pro" }
	if msg := c.familyRefusal(fam, "enso", "acme", nil); msg != "" {
		t.Fatalf("paid caller refusal = %q, want none", msg)
	}
}
