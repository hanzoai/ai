// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"strings"
	"testing"

	iam "github.com/hanzoai/iam"
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
