// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
	"github.com/zap-proto/zip"
)

// vendor is an OpenRouter double whose list a test changes between syncs.
type vendor struct {
	mu   sync.Mutex
	body string
	hits atomic.Int32
}

func (v *vendor) set(body string) {
	v.mu.Lock()
	v.body = body
	v.mu.Unlock()
}

// listingVendor points the OpenRouter family at a vendor double serving body, with a
// store of its own and a recorder for what the bus is told.
func listingVendor(t *testing.T, body string) (*vendor, *[]object.ListingEvent) {
	t.Helper()
	v := &vendor{body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		v.hits.Add(1)
		v.mu.Lock()
		defer v.mu.Unlock()
		_, _ = io.WriteString(w, v.body)
	}))
	t.Cleanup(srv.Close)
	withFamily(t, openrouterFam, "")
	t.Setenv(openrouterFam.urlKey, srv.URL)
	var mu sync.Mutex
	told := &[]object.ListingEvent{}
	object.SetListingPublisher(func(_ context.Context, evs []object.ListingEvent) {
		mu.Lock()
		defer mu.Unlock()
		*told = append(*told, evs...)
	})
	t.Cleanup(func() { object.SetListingPublisher(nil) })
	return v, told
}

// syncNow runs a sync as the next interval's would.
func syncNow(t *testing.T) *synced {
	t.Helper()
	s, err := openrouterFam.syncEvery(context.Background(), openrouterFam.provider(), 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("the sync did not take its interval")
	}
	return s
}

const (
	haiku = `{"id":"anthropic/claude-haiku-5.5","name":"Anthropic: Claude Haiku 5.5","created":1791397883,` +
		`"context_length":1000000,"architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"]},` +
		`"pricing":{"prompt":"0.0000001","completion":"0.0000005","web_search":"0.01","input_cache_read":"0.00000001",` +
		`"input_cache_write":"0.000000125","overrides":[{"min_prompt_tokens":100000,"prompt":"0.0000005","completion":"0.0000025","input_cache_read":"0.00000005"}]},` +
		`"top_provider":{"context_length":1000000,"max_completion_tokens":64000,"is_moderated":false},` +
		`"supported_parameters":["tools","reasoning","structured_outputs"],"expiration_date":null}`
	gemma = `{"id":"google/gemma-4-31b-it:free","context_length":262144,` +
		`"architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},` +
		`"pricing":{"prompt":"0","completion":"0"},"top_provider":{"max_completion_tokens":8192},` +
		`"supported_parameters":["tools"],"expiration_date":"2026-12-31"}`
	laguna = `{"id":"poolside/laguna-s-2.1","context_length":131072,` +
		`"architecture":{"input_modalities":["text"],"output_modalities":["text"]},` +
		`"pricing":{"prompt":"0.0000003","completion":"0.0000012"}}`
)

func listOf(items ...string) string { return `{"data":[` + strings.Join(items, ",") + `]}` }

// A sync keeps every model the vendor lists, each entry exactly as listed, and the
// family serves the store's list: what OpenRouter lists is what we list, priced at
// its rates times the margin, its free models marked free.
func TestASyncKeepsEveryModelAsListed(t *testing.T) {
	_, told := listingVendor(t, listOf(haiku, gemma, laguna))
	t.Setenv("OPENROUTER_MARGIN", "1.2")
	s := syncNow(t)
	if s.listed != 3 || s.count(object.ListingNew) != 3 {
		t.Fatalf("synced %d with %d new, want 3 and 3", s.listed, s.count(object.ListingNew))
	}
	rows, err := object.Listings("openrouter")
	if err != nil || len(rows) != 3 {
		t.Fatalf("store holds %d rows (%v), want 3", len(rows), err)
	}
	for _, r := range rows {
		want := map[string]string{"anthropic/claude-haiku-5.5": haiku, "google/gemma-4-31b-it:free": gemma, "poolside/laguna-s-2.1": laguna}[r.ID]
		if r.Item != want {
			t.Errorf("%s kept as\n %s\nlisted as\n %s", r.ID, r.Item, want)
		}
		if r.Free != (r.ID == "google/gemma-4-31b-it:free") {
			t.Errorf("%s free = %v", r.ID, r.Free)
		}
	}
	if len(*told) != 0 {
		t.Errorf("a sync that seeded the store told the bus %d changes, want none", len(*told))
	}

	openrouterFam.mu.Lock()
	openrouterFam.loaded = false
	openrouterFam.mu.Unlock()
	for _, id := range []string{"anthropic/claude-haiku-5.5", "google/gemma-4-31b-it:free", "poolside/laguna-s-2.1"} {
		if !openrouterFam.serves(id) {
			t.Errorf("%s is listed and not served", id)
		}
	}
	m, _ := openrouterFam.lookup("anthropic/claude-haiku-5.5")
	if m.MaxOut != 64000 || m.Card == nil || m.Card.Rates["web_search"] != "0.012" || m.Card.Rates["input_cache_write"] != "0.00000015" {
		t.Errorf("haiku lost its contract: max out %d, card %+v", m.MaxOut, m.Card)
	}
	if g, _ := openrouterFam.lookup("google/gemma-4-31b-it:free"); g.Expires != "2026-12-31" || g.priced() {
		t.Errorf("gemma expires %q priced %v", g.Expires, g.priced())
	}
}

// What a sync reads changing is recorded and told: a new model, a repricing, a
// model turned from free to paid and back, a model gone and back. A gone model is
// served no longer, and its row stays.
func TestASyncRecordsWhatChanged(t *testing.T) {
	v, told := listingVendor(t, listOf(haiku, gemma, laguna))
	syncNow(t)

	repriced := strings.Replace(laguna, `"prompt":"0.0000003"`, `"prompt":"0.00000035"`, 1)
	paid := strings.Replace(gemma, `"prompt":"0","completion":"0"`, `"prompt":"0.0000001","completion":"0.0000002"`, 1)
	v.set(listOf(paid, repriced))
	s := syncNow(t)
	kinds := map[string]string{}
	for _, e := range s.events {
		kinds[e.ID] = e.Kind
	}
	want := map[string]string{"google/gemma-4-31b-it:free": object.ListingPaid, "poolside/laguna-s-2.1": object.ListingPrice, "anthropic/claude-haiku-5.5": object.ListingGone}
	for id, k := range want {
		if kinds[id] != k {
			t.Errorf("%s: %q, want %q", id, kinds[id], k)
		}
	}
	if len(s.events) != 3 {
		t.Errorf("%d changes, want 3: %+v", len(s.events), s.events)
	}

	openrouterFam.mu.Lock()
	openrouterFam.loaded = false
	openrouterFam.mu.Unlock()
	if openrouterFam.serves("anthropic/claude-haiku-5.5") {
		t.Error("a model the vendor dropped is still served")
	}
	rows, _ := object.Listings("openrouter")
	gone := false
	for _, r := range rows {
		if r.ID == "anthropic/claude-haiku-5.5" {
			gone = r.Gone != ""
		}
	}
	if !gone {
		t.Error("a dropped model's row was not kept, marked gone")
	}

	v.set(listOf(haiku, gemma, repriced))
	s = syncNow(t)
	kinds = map[string]string{}
	for _, e := range s.events {
		kinds[e.ID] = e.Kind
	}
	if kinds["anthropic/claude-haiku-5.5"] != object.ListingBack || kinds["google/gemma-4-31b-it:free"] != object.ListingFree || len(s.events) != 2 {
		t.Errorf("the third sync read %+v", s.events)
	}
	if len(*told) != 3+2 {
		t.Errorf("the bus was told %d changes, want the 5 after the seeding sync", len(*told))
	}

	evs, err := object.ListingEvents("openrouter", "google/gemma-4-31b-it:free", 10)
	if err != nil || len(evs) != 3 || evs[0].Kind != object.ListingFree || evs[2].Kind != object.ListingNew {
		t.Fatalf("gemma's history %+v (%v), want free, paid, new", evs, err)
	}
	if !strings.Contains(evs[1].Now, `"prompt":"0.0000001"`) || !strings.Contains(evs[1].Was, `"prompt":"0"`) {
		t.Errorf("a turn to paid states its prices: was %s now %s", evs[1].Was, evs[1].Now)
	}
}

// A price the vendor spells differently is the same price: no change is read.
func TestASpellingIsNoChange(t *testing.T) {
	v, _ := listingVendor(t, listOf(laguna))
	syncNow(t)
	v.set(listOf(strings.Replace(laguna, `"prompt":"0.0000003"`, `"prompt":"0.00000030"`, 1)))
	if s := syncNow(t); len(s.events) != 0 {
		t.Errorf("a respelled price read as %+v", s.events)
	}
}

// A list of nothing is a list gone wrong: it marks nothing gone.
func TestAnEmptyListChangesNothing(t *testing.T) {
	v, _ := listingVendor(t, listOf(laguna))
	syncNow(t)
	v.set(`{"data":[]}`)
	if _, err := openrouterFam.syncEvery(context.Background(), openrouterFam.provider(), 0, time.Now()); err == nil {
		t.Fatal("an empty list synced")
	}
	rows, _ := object.Listings("openrouter")
	if len(rows) != 1 || rows[0].Gone != "" {
		t.Errorf("an empty list changed the store: %+v", rows)
	}
}

// One process syncs an interval; the rest read what it wrote. A sync that fails
// gives the interval back.
func TestOneProcessSyncsAnInterval(t *testing.T) {
	v, _ := listingVendor(t, listOf(laguna))
	now := time.Now()
	if s, err := openrouterFam.syncEvery(context.Background(), openrouterFam.provider(), listingEvery, now); err != nil || s == nil {
		t.Fatalf("the first process did not sync: %v", err)
	}
	if s, err := openrouterFam.syncEvery(context.Background(), openrouterFam.provider(), listingEvery, now.Add(time.Minute)); err != nil || s != nil {
		t.Fatalf("a second process synced the same interval: %v", err)
	}
	if got := v.hits.Load(); got != 1 {
		t.Errorf("the vendor was asked %d times in one interval", got)
	}
	v.set(`{"data":[]}`)
	later := now.Add(listingEvery + time.Minute)
	if _, err := openrouterFam.syncEvery(context.Background(), openrouterFam.provider(), listingEvery, later); err == nil {
		t.Fatal("a failing sync reported none")
	}
	v.set(listOf(laguna))
	if s, err := openrouterFam.syncEvery(context.Background(), openrouterFam.provider(), listingEvery, later.Add(time.Second)); err != nil || s == nil {
		t.Fatalf("a failed sync kept its interval: %v", err)
	}
}

// The list and its history are public, in the vendor's own shape: the free lane
// reads ?free=1 in place of the vendor's list.
func TestTheListingIsServedAsTheVendorListsIt(t *testing.T) {
	listingVendor(t, listOf(haiku, gemma, laguna))
	syncNow(t)

	status, body := fetched(t, "/v1/listings/:vendor", "/v1/listings/openrouter?free=1", (*ApiController).ListListing)
	var got struct {
		Vendor string            `json:"vendor"`
		Synced string            `json:"synced"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || status != http.StatusOK {
		t.Fatalf("answered %d %s", status, body)
	}
	if got.Vendor != "openrouter" || got.Synced == "" || len(got.Data) != 1 || string(got.Data[0]) != gemma {
		t.Errorf("free listing %+v", got)
	}

	_, body = fetched(t, "/v1/listings/:vendor/events", "/v1/listings/openrouter/events", (*ApiController).ListListingEvents)
	var evs struct {
		Data []object.ListingEvent `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &evs); err != nil || len(evs.Data) != 3 {
		t.Errorf("history answered %s", body)
	}

	if status, _ := fetched(t, "/v1/listings/:vendor", "/v1/listings/nobody", (*ApiController).ListListing); status != http.StatusNotFound {
		t.Errorf("an unlisted vendor answered %d", status)
	}
}

// fetched answers a GET of target through a route of pattern, so the handler reads
// its path parameters as it does when served.
func fetched(t *testing.T, pattern, target string, h func(*ApiController)) (int, string) {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(http.MethodGet, pattern, func(c *zip.Ctx) error {
		h(&ApiController{Ctx: c})
		return nil
	})
	res, err := app.Test(httptest.NewRequest(http.MethodGet, target, nil), zip.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// /v1/models lists a resold model with every rate its vendor states, each times the
// margin exactly, its conditional rates beside them, and says whether it is free.
func TestTheCatalogPublishesEveryRateExactly(t *testing.T) {
	listingVendor(t, listOf(haiku, gemma))
	t.Setenv("OPENROUTER_MARGIN", "1.2")
	syncNow(t)
	openrouterFam.mu.Lock()
	openrouterFam.loaded = false
	openrouterFam.mu.Unlock()
	openrouterFam.fresh()

	byID := map[string]modelInfo{}
	for _, m := range openrouterFam.mergeModels(nil) {
		byID[m.ID] = m
	}
	h := byID["anthropic/claude-haiku-5.5"]
	h.Pricing = pricingInfo(openrouterFam.modelPrice(h.ID))
	if h.MaxOutputTokens != 64000 || h.Pricing == nil || h.Pricing.Prompt != "0.00000012" || h.Pricing.Completion != "0.0000006" ||
		h.Pricing.Rates["input_cache_read"] != "0.000000012" || h.Pricing.Rates["web_search"] != "0.012" || len(h.Pricing.Overrides) != 1 {
		t.Fatalf("haiku listed as %+v %+v", h, h.Pricing)
	}
	if o := h.Pricing.Overrides[0]; o["prompt"] != "0.0000006" || fmt.Sprint(o["min_prompt_tokens"]) != "100000" {
		t.Errorf("haiku's override listed as %v", o)
	}
	g := byID["google/gemma-4-31b-it:free"]
	if g.Expires != "2026-12-31" || g.Premium {
		t.Errorf("gemma listed as %+v", g)
	}
}

// A resold SKU bills at the tier its prompt reaches, the dearer side of an
// override's line, and a cached token at the cache rate. A discount by hours of the
// day is never billed, so no call bills below the base.
func TestAnOverrideBillsTheDearerTier(t *testing.T) {
	var w openrouterWireModel
	body := strings.Replace(haiku, `"overrides":[`, `"overrides":[{"utc_start":0,"utc_end":800,"prompt":"0.00000001"},`, 1)
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		t.Fatal(err)
	}
	m := w.model(decimal.MustParse("1.2"))
	if len(m.Tiers) != 2 || m.Tiers[0].MaxCtx != 99999 {
		t.Fatalf("tiers %+v", m.Tiers)
	}
	if got := m.bill(nil, 99999, 0, 0); got != nanoUp(decimal.MustParse("0.00000012").Mul(decimal.New(99999, 0))) {
		t.Errorf("a prompt under the line billed %d", got)
	}
	if got := m.bill(nil, 100000, 0, 0); got != nanoUp(decimal.MustParse("0.0000006").Mul(decimal.New(100000, 0))) {
		t.Errorf("a prompt at the line billed %d", got)
	}
	if got := m.bill(nil, 0, 1000, 0); got != nanoUp(decimal.MustParse("0.000000012").Mul(decimal.New(1000, 0))) {
		t.Errorf("a thousand cached tokens billed %d", got)
	}
}

// The free view is the free models listed now, each with what the last day did with
// it and which of our accounts served it.
func TestTheFreeViewCountsEachAccount(t *testing.T) {
	listingVendor(t, listOf(haiku, gemma))
	syncNow(t)
	now := time.Now()
	for _, c := range []struct {
		account string
		ok      bool
	}{{"OPENROUTER_API_KEY", true}, {"OPENROUTER_API_KEY", true}, {"OPENROUTER_API_KEY_2", false}, {"OPENROUTER_API_KEY_2", true}} {
		if err := object.CountServed("openrouter", "google/gemma-4-31b-it:free", c.account, now, c.ok); err != nil {
			t.Fatal(err)
		}
	}
	if err := object.CountServed("openrouter", "google/gemma-4-31b-it:free", "OPENROUTER_API_KEY", now.Add(-25*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	set, err := freeSet("openrouter", now)
	if err != nil || len(set) != 1 {
		t.Fatalf("free set %+v (%v), want gemma alone", set, err)
	}
	g := set[0]
	if g.ID != "google/gemma-4-31b-it:free" || g.Answered != 3 || g.Failed != 1 || g.Rate == nil || *g.Rate != 0.75 || len(g.Accounts) != 2 {
		t.Fatalf("gemma's day %+v", g)
	}
	if g.Accounts[1] != (freeAccount{Account: "OPENROUTER_API_KEY_2", Answered: 1, Failed: 1}) {
		t.Errorf("the second account's day %+v", g.Accounts[1])
	}
}

// Each free request ai sends counts toward the account it went on: refused on one,
// answered on the next.
func TestAFreeRequestCountsTheAccountItWentOn(t *testing.T) {
	listingVendor(t, listOf(gemma))
	syncNow(t)
	t.Setenv("OPENROUTER_API_KEY", "k-one")
	t.Setenv("OPENROUTER_API_KEY_2", "k-two")
	do := func(r *http.Request) (*http.Response, error) {
		status := http.StatusOK
		if r.Header.Get("Authorization") == "Bearer k-one" {
			status = http.StatusTooManyRequests
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	r, _ := http.NewRequest(http.MethodPost, "http://vendor/v1/chat/completions", strings.NewReader(`{"model":"google/gemma-4-31b-it:free"}`))
	count := openrouterFam.counting(r, do)
	for _, k := range []string{"k-one", "k-two"} {
		rr := r.Clone(context.Background())
		rr.Header.Set("Authorization", "Bearer "+k)
		if _, err := count(rr); err != nil {
			t.Fatal(err)
		}
	}
	var rows []object.Served
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		rows, _ = object.ServedSince("openrouter", time.Now().Add(-time.Hour))
		if len(rows) == 2 {
			break
		}
	}
	got := map[string][2]int64{}
	for _, s := range rows {
		got[s.Account] = [2]int64{s.Answered, s.Failed}
	}
	if got["OPENROUTER_API_KEY"] != [2]int64{0, 1} || got["OPENROUTER_API_KEY_2"] != [2]int64{1, 0} {
		t.Errorf("served %+v", rows)
	}
}
