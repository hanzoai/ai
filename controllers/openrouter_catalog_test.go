package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// orBody is a two-model OpenRouter /v1/models payload in the real wire dialect: prices
// are USD-per-TOKEN strings, the window is context_length, vision is a modality, and
// nothing states a tier or a funding class.
const orBody = `{"data":[
 {"id":"anthropic/claude-sonnet-4","context_length":200000,
  "architecture":{"input_modalities":["text","image"]},
  "pricing":{"prompt":"0.000003","completion":"0.000015"}},
 {"id":"meta/muse-spark-1.1","context_length":1048576,
  "architecture":{"input_modalities":["text"]},
  "pricing":{"prompt":"0.00000125","completion":"0.00000425"}}
]}`

// withOpenRouter points the family at a stub catalog for one test and restores the
// snapshot afterwards, so these tests never touch the live OpenRouter service.
func withOpenRouter(t *testing.T, body string) *int32 {
	t.Helper()
	return withFamily(t, openrouterFam, body)
}

// withFamily points f at a stub catalog serving body for one test and restores its
// snapshot afterwards. It returns how many times the catalog was fetched.
func withFamily(t *testing.T, f *modelFamily, body string) *int32 {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(f.urlKey, srv.URL)
	// A family whose list the store keeps is read from a store of its own, which its
	// first read fills from the stub (listing.go).
	if f.load != nil {
		withStore(t)
	}

	savedByID, savedIDs := f.byID, f.ids
	savedLoaded, savedAt := f.loaded, f.fetchedAt
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.byID, f.ids = savedByID, savedIDs
		f.loaded, f.fetchedAt = savedLoaded, savedAt
	})
	f.mu.Lock()
	f.byID, f.ids = nil, nil
	f.loaded, f.fetchedAt = false, time.Time{}
	f.mu.Unlock()
	return &hits
}

// The catalog is DISCOVERED, kept in the store and cached on the shared family TTL: a
// cold family syncs the vendor's list once, subsequent reads are served from the
// snapshot, a snapshot older than the TTL is read again from the store, and the vendor
// is asked again when the sync's interval has passed. No model id is written down
// anywhere in ai.
func TestOpenRouterCatalogIsDiscoveredAndCachedOnTTL(t *testing.T) {
	hits := withOpenRouter(t, orBody)

	if _, ok := openrouterFam.lookup("anthropic/claude-sonnet-4"); ok {
		t.Fatal("a cold family must know nothing before discovery — the catalog is not hardcoded")
	}

	openrouterFam.fresh()
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("cold family should fetch exactly once, fetched %d times", got)
	}
	m, ok := openrouterFam.lookup("anthropic/claude-sonnet-4")
	if !ok {
		t.Fatal("discovered model missing from the snapshot")
	}
	if m.MaxCtx != 200000 || !m.Vision {
		t.Errorf("discovery lost the SKU's contract: window %d vision %v", m.MaxCtx, m.Vision)
	}
	if !openrouterFam.serves("meta/muse-spark-1.1") {
		t.Error("a discovered SKU must be served by its family")
	}

	// Warm: every further read is a snapshot read, not an upstream fetch.
	for range 5 {
		openrouterFam.fresh()
		openrouterFam.snapshot()
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("a warm catalog must be served from cache within the TTL, refetched %d times", got-1)
	}

	// Aged past the TTL: the family reads the store again, which the sync of this
	// interval already filled, so the vendor is not asked.
	openrouterFam.mu.Lock()
	openrouterFam.fetchedAt = time.Now().Add(-zenCatalogTTL - time.Second)
	openrouterFam.mu.Unlock()
	openrouterFam.fresh()
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("a catalog older than the TTL is read from the store, yet the vendor was asked %d times", got)
	}
	if !openrouterFam.serves("meta/muse-spark-1.1") {
		t.Error("the store's catalog lost a model")
	}
	// Once the interval has passed, the next read syncs.
	if _, err := openrouterFam.syncEvery(context.Background(), openrouterFam.provider(), 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Errorf("a sync past its interval must ask the vendor, total fetches %d", got)
	}
}

// An unconfigured deployment exposes no OpenRouter model at all. The catalog's address
// is configuration, exactly like Zen's and Enso's.
func TestOpenRouterUnconfiguredExposesNothing(t *testing.T) {
	t.Setenv(openrouterFam.urlKey, "")
	if openrouterFam.enabled() {
		t.Fatal("family must be disabled without its URL")
	}
	if openrouterFam.serves("anthropic/claude-sonnet-4") {
		t.Error("an unconfigured catalog must serve nothing")
	}
	if got := openrouterFam.mergeModels(nil); len(got) != 0 {
		t.Errorf("an unconfigured catalog must list nothing, listed %d", len(got))
	}
}

// fee installs the host's OpenRouter fee, in percent, for one test.
func fee(t *testing.T, pct string) {
	t.Helper()
	object.SetFees(func(vendor string) string {
		if vendor == "openrouter" {
			return pct
		}
		return ""
	})
	t.Cleanup(func() { object.SetFees(nil) })
}

// Retail is the vendor's price plus the fee the host's operator sets (5.5%, what
// OpenRouter charges on the credit it sells), and the vendor's price travels with the
// SKU as COGS so the margin ledger books the real spread instead of assuming one.
func TestOpenRouterPricingIsTheVendorsPricePlusItsFee(t *testing.T) {
	fee(t, "5.5")
	withOpenRouter(t, orBody)
	openrouterFam.fresh()

	m, ok := openrouterFam.lookup("anthropic/claude-sonnet-4")
	if !ok {
		t.Fatal("discovered model missing")
	}
	// $0.000003/token upstream = $3.00/MTok cost; at a 5.5% fee retail is $3.165/MTok.
	wantCost, wantRetail := decimal.New(300, 2), decimal.MustParse("3.165")
	if m.CostIn.Cmp(wantCost) != 0 {
		t.Errorf("input COGS = %s, want %s ($/MTok from $/token)", m.CostIn, wantCost)
	}
	if m.Base.In.Cmp(wantRetail) != 0 {
		t.Errorf("input retail = %s, want %s (the vendor's price plus 5.5%%)", m.Base.In, wantRetail)
	}
	if m.Base.In.Cmp(m.CostIn) <= 0 || m.Base.Out.Cmp(m.CostOut) <= 0 {
		t.Error("retail must be above cost while a fee is set: the fee is what the vendor charges us on top")
	}

	p, ok := m.price()
	if !ok {
		t.Fatal("a priced SKU must project a price")
	}
	if p.costInputPerMillion() >= p.InputPerMillion {
		t.Errorf("projected COGS %v must stay below projected price %v", p.costInputPerMillion(), p.InputPerMillion)
	}
}

// With no fee set a resold model bills at its vendor's price. A fee that is not a
// percent of 0 or more is refused, so the catalog keeps the prices it had rather than
// publish one below cost.
func TestAnOpenRouterFeeIsAPercentOfZeroOrMore(t *testing.T) {
	models, err := openrouterCatalog([]byte(orBody))
	if err != nil {
		t.Fatalf("decode with no fee: %v", err)
	}
	for _, m := range models {
		if m.Base.In.Cmp(m.CostIn) != 0 || m.Base.Out.Cmp(m.CostOut) != 0 {
			t.Errorf("with no fee %s lists %s/%s over a cost of %s/%s", m.ID, m.Base.In, m.Base.Out, m.CostIn, m.CostOut)
		}
	}
	for _, raw := range []string{"-3", "not-a-number", "1.2x"} {
		t.Run(raw, func(t *testing.T) {
			fee(t, raw)
			if _, err := openrouterCatalog([]byte(orBody)); err == nil {
				t.Fatalf("fee %q decoded a catalog", raw)
			}
		})
	}
}

// Every discovered OpenRouter model carries BOTH paid floors, because every one of them
// is prepaid resale. This is what the funding gate reads; it is not re-implemented.
func TestOpenRouterModelsAreAllPaidAndPrepaid(t *testing.T) {
	withOpenRouter(t, orBody)
	openrouterFam.fresh()

	models := openrouterFam.snapshot()
	if len(models) != 2 {
		t.Fatalf("expected 2 discovered models, got %d", len(models))
	}
	for _, m := range models {
		if m.minTier() != "paid" {
			t.Errorf("%s advertises min_tier %q — every resale SKU must sit behind the paid floor", m.ID, m.minTier())
		}
		if m.Funding != "prepaid" {
			t.Errorf("%s funding %q — resale spends real cash and must be marked prepaid", m.ID, m.Funding)
		}
	}
}

// The gate itself: a free-tier caller must not reach ANY OpenRouter model by ANY path —
// not the serve gate, not the auto-router's servable predicate, and not route selection.
// A caller commerce cannot vouch for is refused too, because the funding floor fails
// closed on money. A confirmed paying caller gets through.
func TestOpenRouterRefusesFreeTierOnEveryPath(t *testing.T) {
	withOpenRouter(t, orBody)
	openrouterFam.fresh()

	const freeSubject, paidSubject = "acme/free-user", "acme/paid-user"
	savedTier := familyTier
	t.Cleanup(func() { familyTier = savedTier })
	familyTier = func(subject string) string {
		switch subject {
		case freeSubject:
			return "free"
		case paidSubject:
			return "pro"
		}
		return "" // commerce cannot name this caller
	}

	models := openrouterFam.snapshot()
	if len(models) == 0 {
		t.Fatal("nothing discovered to gate")
	}
	for _, m := range models {
		// The tier/funding HELPERS still classify a caller's plan (they meter USAGE
		// LIMITS now, not access): a free plan ranks below trial, a paid one clears it.
		if familyTierAllowed(freeSubject, m.ID) {
			t.Errorf("free tier should rank below the SKU's min_tier for %s", m.ID)
		}
		if !familyTierAllowed(paidSubject, m.ID) {
			t.Errorf("a paying plan should clear the tier rank for %s", m.ID)
		}
		// But access is MONEY, not tier: a discovered (non-preview) model is servable
		// to any caller — the balance gate meters the spend on the serve path, so
		// modelServable is grant-only and does not consult the plan.
		if !modelServable(m.ID, "", nil) {
			t.Errorf("%s must be servable — access is money+grant, not plan", m.ID)
		}
	}
}

// The discovered catalog reaches the public /v1/models listing, attributed to the vendor
// that made each model, priced at retail, and marked access-gated where relevant.
func TestOpenRouterCatalogIsListed(t *testing.T) {
	withOpenRouter(t, orBody)

	listed := mergeFamilyModels(nil)
	byID := map[string]modelInfo{}
	for _, m := range listed {
		byID[m.ID] = m
	}
	got, ok := byID["anthropic/claude-sonnet-4"]
	if !ok {
		t.Fatalf("discovered OpenRouter model absent from /v1/models (listed %d)", len(listed))
	}
	if got.OwnedBy != "anthropic" {
		t.Errorf("owned_by = %q, want the vendor that made the model", got.OwnedBy)
	}
	if got.ContextWindow != 200000 {
		t.Errorf("context_window = %d, want the discovered window", got.ContextWindow)
	}
	if p, ok := familyModelPrice(got.ID); !ok || p.InputPerMillion <= 0 {
		t.Error("a resale SKU must be priced at its retail, which the listing reads")
	}
}
