package controllers

import (
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// The subscription floor and the funding floor must fail in OPPOSITE directions.
//
// A subscription floor is an upsell: if commerce cannot name the caller's plan, admitting
// them costs one free taste, and refusing would lock a paying customer out of a SKU they
// already had. A funding floor is solvency: if commerce cannot name the plan, admitting
// them spends OUR CASH on someone who may not be paying us, and draining a prepaid balance
// takes the SKU down for every paying customer.
//
// These were a single fused value (min_tier) until the family started advertising
// `funding` separately, which is exactly how the funding floor inherited the upsell
// floor's fail-open behaviour.
func TestFundingGateFailsClosedWhereTierGateFailsOpen(t *testing.T) {
	const prepaidSKU, creditSKU = "enso-ultra-test-prepaid", "enso-test-credit"

	// The gate only speaks for models a family SERVES, which requires the family to be
	// configured — otherwise these ids are ordinary non-family models and out of scope.
	t.Setenv(ensoFam.urlKey, "http://enso.invalid")
	saved, savedLoaded, savedAt := ensoFam.byID, ensoFam.loaded, ensoFam.fetchedAt
	t.Cleanup(func() { ensoFam.byID, ensoFam.loaded, ensoFam.fetchedAt = saved, savedLoaded, savedAt })
	ensoFam.byID = map[string]zenModel{
		prepaidSKU: {ID: prepaidSKU, MinTier: "paid", Funding: "prepaid"},
		creditSKU:  {ID: creditSKU, MinTier: "paid", Funding: ""},
	}
	ensoFam.loaded, ensoFam.fetchedAt = true, time.Now()

	// An unresolvable subject stands for every uncertainty the gates share: commerce
	// unconfigured, transport error, non-2xx, decode failure, timeout.
	const unknownSubject = ""

	if !familyTierAllowed(unknownSubject, prepaidSKU) {
		t.Error("subscription gate must FAIL OPEN on an unresolvable tier — a commerce blip must not lock out a paying caller")
	}
	if familyFundingAllowed(unknownSubject, prepaidSKU) {
		t.Error("funding gate must FAIL CLOSED on an unresolvable tier — we must not spend cash on a guess")
	}

	// A credit-funded SKU is untouched by the funding gate: only the subscription floor
	// applies, so grants keep serving the free tier exactly as before.
	if !familyFundingAllowed(unknownSubject, creditSKU) {
		t.Error("a credit-funded SKU must not be gated on funding — grants may serve any tier")
	}

	// A SKU discovery cannot describe: the subscription gate treats it as ungated and
	// admits, but its funding is unvouched, so the funding gate must refuse. The prefix
	// router would otherwise still serve it.
	if !familyTierAllowed(unknownSubject, "enso-unknown-to-discovery") {
		t.Error("subscription gate: unknown SKU is not tier-gated (fail open)")
	}
	if familyFundingAllowed(unknownSubject, "enso-unknown-to-discovery") {
		t.Error("funding gate must refuse a SKU whose funding discovery cannot vouch for")
	}
}

// Route SELECTION must not be confused with authorization. An empty subject is the direct
// path resolving its authoritative route; dropping it to nil there leaves the request with
// no route at all instead of deferring to the access gate — which is where the fail-closed
// funding decision actually belongs.
func TestUnnamedCallerStillResolvesAFamilyRoute(t *testing.T) {
	const prepaidSKU = "enso-ultra-test-prepaid"

	t.Setenv(ensoFam.urlKey, "http://enso.invalid")
	saved, savedLoaded, savedAt := ensoFam.byID, ensoFam.loaded, ensoFam.fetchedAt
	t.Cleanup(func() { ensoFam.byID, ensoFam.loaded, ensoFam.fetchedAt = saved, savedLoaded, savedAt })
	ensoFam.byID = map[string]zenModel{prepaidSKU: {ID: prepaidSKU, MinTier: "paid", Funding: "prepaid"}}
	ensoFam.loaded, ensoFam.fetchedAt = true, time.Now()

	if got := resolveModelRouteForOrg(prepaidSKU, ""); got == nil {
		t.Error("a prepaid family SKU lost its route for an unnamed caller — the funding refusal belongs at the serve gate, not route selection")
	}
}

// A program's call for a customer seats on a priced model — a Zen or Enso tier or
// another vendor's — only for a customer with a paid plan: free users get free
// models, whoever asks for them.
func TestAProgramsCallForAFreeCustomerIsNeverPaid(t *testing.T) {
	const sku = "enso-ultra-test-paid"
	t.Setenv(ensoFam.urlKey, "http://enso.invalid")
	saved, savedLoaded, savedAt := ensoFam.byID, ensoFam.loaded, ensoFam.fetchedAt
	t.Cleanup(func() { ensoFam.byID, ensoFam.loaded, ensoFam.fetchedAt = saved, savedLoaded, savedAt })
	ensoFam.byID = map[string]zenModel{
		sku:              {ID: sku, MinTier: "paid", Funding: "prepaid", Base: zenTier{In: decimal.MustParse("6.6"), Out: decimal.MustParse("33")}},
		"enso-free-test": {ID: "enso-free-test"},
	}
	ensoFam.loaded, ensoFam.fetchedAt = true, time.Now()
	prev := familyTier
	t.Cleanup(func() { familyTier = prev })
	plans := map[string]string{"hanzo/alice": "free", "hanzo/bob": "max-20x", "acme": "team"}
	familyTier = func(subject string) string { return plans[subject] }
	for _, model := range []string{sku, "anthropic/claude-opus-5.5"} {
		for _, c := range []struct {
			who  object.Caller
			want bool
		}{
			{object.Caller{Org: "hanzo", Person: "hanzo/alice"}, false},
			{object.Caller{Org: "hanzo", Person: "hanzo/bob"}, true},
			{object.Caller{Org: "acme", Person: "acme/carol"}, true},
			{object.Caller{Org: "hanzo", Person: "hanzo/nobody"}, false},
		} {
			if got := PaidCustomer(c.who, model); got != c.want {
				t.Errorf("PaidCustomer(%+v, %s) = %v, want %v", c.who, model, got, c.want)
			}
		}
	}
	if !PaidCustomer(object.Caller{Org: "hanzo", Person: "hanzo/alice"}, "enso-free-test") {
		t.Error("a free model was refused a free customer")
	}
}
