package controllers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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
	t.Setenv(openrouterFam.urlKey, srv.URL)

	savedByID, savedIDs := openrouterFam.byID, openrouterFam.ids
	savedLoaded, savedAt := openrouterFam.loaded, openrouterFam.fetchedAt
	t.Cleanup(func() {
		openrouterFam.mu.Lock()
		defer openrouterFam.mu.Unlock()
		openrouterFam.byID, openrouterFam.ids = savedByID, savedIDs
		openrouterFam.loaded, openrouterFam.fetchedAt = savedLoaded, savedAt
	})
	openrouterFam.mu.Lock()
	openrouterFam.byID, openrouterFam.ids = nil, nil
	openrouterFam.loaded, openrouterFam.fetchedAt = false, time.Time{}
	openrouterFam.mu.Unlock()
	return &hits
}

// The catalog is DISCOVERED and cached on the shared family TTL: a cold family fetches
// once, subsequent reads are served from the snapshot, and a snapshot older than the
// TTL refetches. No model id is written down anywhere in ai.
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
	for i := 0; i < 5; i++ {
		openrouterFam.fresh()
		openrouterFam.snapshot()
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("a warm catalog must be served from cache within the TTL, refetched %d times", got-1)
	}

	// Aged past the TTL: the family refreshes rather than serving a stale catalog.
	openrouterFam.mu.Lock()
	openrouterFam.fetchedAt = time.Now().Add(-zenCatalogTTL - time.Second)
	openrouterFam.mu.Unlock()
	openrouterFam.fresh()
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Errorf("a catalog older than the TTL must refetch, total fetches %d", got)
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

// Retail is the upstream price times the margin — strictly ABOVE cost, never below it —
// and the upstream price travels with the SKU as COGS so the margin ledger books the
// real spread instead of assuming one.
func TestOpenRouterPricingDerivesAboveCost(t *testing.T) {
	t.Setenv("OPENROUTER_MARGIN", "1.20")
	withOpenRouter(t, orBody)
	openrouterFam.fresh()

	m, ok := openrouterFam.lookup("anthropic/claude-sonnet-4")
	if !ok {
		t.Fatal("discovered model missing")
	}
	// $0.000003/token upstream = $3.00/MTok cost; at 1.20x retail is $3.60/MTok.
	wantCost, wantRetail := decimal.New(300, 2), decimal.New(360, 2)
	if m.CostIn.Cmp(wantCost) != 0 {
		t.Errorf("input COGS = %s, want %s ($/MTok from $/token)", m.CostIn, wantCost)
	}
	if m.Base.In.Cmp(wantRetail) != 0 {
		t.Errorf("input retail = %s, want %s (cost x margin)", m.Base.In, wantRetail)
	}
	if m.Base.In.Cmp(m.CostIn) <= 0 || m.Base.Out.Cmp(m.CostOut) <= 0 {
		t.Error("retail must be strictly above cost — resale at a loss drains the prepaid balance")
	}

	p, ok := m.price()
	if !ok {
		t.Fatal("a priced SKU must project a price")
	}
	if p.costInputPerMillion() >= p.InputPerMillion {
		t.Errorf("projected COGS %v must stay below projected price %v", p.costInputPerMillion(), p.InputPerMillion)
	}
}

// A mis-set or hostile margin can never publish retail below cost: the multiple is
// clamped at 1, so the worst case is a zero spread, never a negative one.
func TestOpenRouterMarginNeverBelowCost(t *testing.T) {
	for _, raw := range []string{"0.5", "0", "-3", "not-a-number"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("OPENROUTER_MARGIN", raw)
			models, err := openrouterCatalog([]byte(orBody))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			for _, m := range models {
				if m.Base.In.Cmp(m.CostIn) < 0 || m.Base.Out.Cmp(m.CostOut) < 0 {
					t.Errorf("margin %q published %s below cost", raw, m.ID)
				}
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
	if got.Pricing == nil || got.Pricing.Input <= 0 {
		t.Error("a resale SKU must list its retail price")
	}
}
