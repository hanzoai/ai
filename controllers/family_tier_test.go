// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/ai/object"
)

// commerceFamilyTier prefers a host-installed native tier reader (cloud's co-resident
// commerce read over commerceinproc) over the authed HTTP self-call that the cloud edge
// 401/403s in-cluster — the fix for the toothless gate. A reader error folds to "" so the
// gate stays fail-safe (unknown → allow). This is the object.TierReader() twin of
// getUserBalance's object.BalanceReader() seam.
func TestCommerceFamilyTierNativeReader(t *testing.T) {
	saved := object.TierReader()
	t.Cleanup(func() { object.SetTierReader(saved) })

	// Installed reader resolves the plan name directly (no HTTP), scoped to the
	// subject's namespace (the "<org>/" prefix of a person subject).
	object.SetTierReader(func(_ context.Context, subject, namespace string) (string, error) {
		if subject == "hanzo/alice" && namespace == "hanzo" {
			return "pro", nil
		}
		return "", nil
	})
	if got := commerceFamilyTier("hanzo/alice"); got != "pro" {
		t.Errorf("native reader tier = %q, want pro", got)
	}

	// A reader error must fold to "" (unknown → the gate allows) — a co-resident
	// commerce blip never locks out a paying caller.
	object.SetTierReader(func(_ context.Context, _, _ string) (string, error) {
		return "", errors.New("commerce unreachable")
	})
	if got := commerceFamilyTier("hanzo/alice"); got != "" {
		t.Errorf("reader error must yield \"\" (fail-safe), got %q", got)
	}
}

// familyTierAllowed enforces the enso subscription ladder (free < trial < paid) against
// each SKU's advertised min_tier, and FAILS SAFE (allows) on any commerce uncertainty.
// The commerce tier lookup is mocked via the injectable familyTier var (as
// orgAutoRoutingLookup / routingEventSink are), so no live commerce is needed.
func TestFamilyTierAllowed(t *testing.T) {
	// Seed the enso family's discovered catalog with the three ladder rungs.
	savedByID := ensoFam.byID
	ensoFam.byID = map[string]zenModel{
		"enso-flash": {ID: "enso-flash", MinTier: ""},     // free — everyone
		"enso":       {ID: "enso", MinTier: "trial"},      // trial and above
		"enso-ultra": {ID: "enso-ultra", MinTier: "paid"}, // paid only
	}
	t.Cleanup(func() { ensoFam.byID = savedByID })

	savedTier := familyTier
	t.Cleanup(func() { familyTier = savedTier })
	setTier := func(name string) { familyTier = func(string) string { return name } }

	const subj = "acme"

	// free caller (commerce "free"): enso-flash only.
	setTier("free")
	if !familyTierAllowed(subj, "enso-flash") {
		t.Error("free caller must access enso-flash (min_tier free)")
	}
	if familyTierAllowed(subj, "enso") {
		t.Error("free caller must NOT access enso (min_tier trial)")
	}
	if familyTierAllowed(subj, "enso-ultra") {
		t.Error("free caller must NOT access enso-ultra (min_tier paid)")
	}

	// trial caller (commerce "starter"): enso-flash + enso, not enso-ultra.
	setTier("starter")
	if !familyTierAllowed(subj, "enso-flash") {
		t.Error("trial caller must access enso-flash")
	}
	if !familyTierAllowed(subj, "enso") {
		t.Error("trial caller must access enso")
	}
	if familyTierAllowed(subj, "enso-ultra") {
		t.Error("trial caller must NOT access enso-ultra")
	}

	// paid caller (commerce "pro"): every SKU.
	setTier("pro")
	for _, m := range []string{"enso-flash", "enso", "enso-ultra"} {
		if !familyTierAllowed(subj, m) {
			t.Errorf("paid (pro) caller must access %s", m)
		}
	}
	// "enterprise" also folds to paid.
	setTier("enterprise")
	if !familyTierAllowed(subj, "enso-ultra") {
		t.Error("enterprise caller must access enso-ultra")
	}

	// commerce could not name the tier ("" — error/timeout/unconfigured) → FAIL SAFE.
	setTier("")
	if !familyTierAllowed(subj, "enso-ultra") {
		t.Error("commerce-unknown must fail safe to allow enso-ultra")
	}

	// a model unknown to discovery is not tier-gated → allow (even for a free caller).
	setTier("free")
	if !familyTierAllowed(subj, "no-such-model") {
		t.Error("model unknown to discovery must be allowed")
	}
}

// The ladder mappers are the policy translation: enso min_tier names and commerce plan
// names both fold onto the same three comparable rungs (free < trial < paid).
func TestEnsoLadderMappers(t *testing.T) {
	if ensoTierRank("free") != 0 || ensoTierRank("") != 0 {
		t.Error(`free and "" must rank 0`)
	}
	if ensoTierRank("trial") != 1 {
		t.Error("trial must rank 1")
	}
	if ensoTierRank("paid") != 2 {
		t.Error("paid must rank 2")
	}

	// A plan is PAID unless it is explicitly free. The old allow-list scored every
	// unrecognized name free, and the names it did not recognize were go, dev, max,
	// team and business — i.e. most of what commerce sells — so paying subscribers
	// were refused SKUs they had funded. An unknown name erring toward paid cannot
	// give anything away: filter_balance.go has no exemptions and fails closed.
	for name, want := range map[string]string{
		"free": "free", "zen-free": "free", "world-free": "free", "": "free",
		"starter": "trial", "trial": "trial",
		"go": "paid", "dev": "paid", "pro": "paid", "max": "paid",
		"team": "paid", "business": "paid", "enterprise": "paid",
		"FREE": "free", " Pro ": "paid", "mystery": "paid",
	} {
		if got := commerceTierToLadder(name); got != want {
			t.Errorf("commerceTierToLadder(%q) = %q, want %q", name, got, want)
		}
	}
}

// minTier normalizes the discovered min_tier: "" ⇒ "free", others lowercased/trimmed.
func TestZenModelMinTier(t *testing.T) {
	for in, want := range map[string]string{
		"": "free", "free": "free", "trial": "trial", "paid": "paid", " PAID ": "paid",
	} {
		if got := (zenModel{MinTier: in}).minTier(); got != want {
			t.Errorf("minTier(%q) = %q, want %q", in, got, want)
		}
	}
	// The wire field decodes onto the model.
	if got := (zenWireModel{ID: "enso", MinTier: "paid"}).model().minTier(); got != "paid" {
		t.Errorf("wire min_tier did not carry onto the model: got %q", got)
	}
}
