// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

import (
	"encoding/json"
	"github.com/hanzoai/decimal"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/web"
	"go.opentelemetry.io/otel/attribute"
)

// A listing with both kinds of SKU in it, deliberately serialized worst-first so
// the ordering assertions below cannot pass by accident.
const spareListing = `{"data":[
 {"id":"vendor/small:free","context_length":8000,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]}},
 {"id":"vendor/paid-a","context_length":128000,"pricing":{"prompt":"0.000002","completion":"0.000008"},"architecture":{"output_modalities":["text"]}},
 {"id":"vendor/big:free","context_length":1000000,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]}},
 {"id":"vendor/paid-b","context_length":64000,"pricing":{"prompt":"0.0000005","completion":"0.0000015"},"architecture":{"output_modalities":["text"]}},
 {"id":"vendor/music:free","context_length":1048576,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text","audio"]}},
 {"id":"vendor/silent:free","context_length":999999,"pricing":{"prompt":"0","completion":"0"}}
]}`

// The catalog is the vendor's whole listing, and the spare routes are a reading of
// the same body — so what we publish is what they serve, and the floors a SKU
// carries are decided by the one fact that matters: its price. A priced SKU takes
// both floors and can never be handed out as a remedy; a free one takes neither and
// bills nothing however a request reaches it.
func TestTheCatalogIsTheListingAndPriceDecidesTheFloors(t *testing.T) {
	catalog, err := openrouterCatalog([]byte(spareListing))
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	spares := openrouterSpare([]byte(spareListing))

	listed := map[string]zenModel{}
	for _, m := range catalog {
		listed[m.ID] = m
	}
	for _, id := range []string{
		"vendor/paid-a", "vendor/paid-b",
		"vendor/small:free", "vendor/big:free", "vendor/music:free", "vendor/silent:free",
	} {
		if _, ok := listed[id]; !ok {
			t.Errorf("%q is advertised by the vendor and absent from the catalog", id)
		}
	}
	if len(listed) != 6 {
		t.Errorf("catalog holds %d SKUs, want the whole listing", len(listed))
	}

	for id, m := range listed {
		free := m.Base.In.IsZero() && m.Base.Out.IsZero()
		switch {
		case free && (m.MinTier != "" || m.Funding != ""):
			t.Errorf("%q costs nothing and carries floors (tier=%q funding=%q) — it would refuse a caller over money never spent", id, m.MinTier, m.Funding)
		case !free && (m.MinTier != "paid" || m.Funding != "prepaid"):
			t.Errorf("%q spends real cash and carries tier=%q funding=%q", id, m.MinTier, m.Funding)
		}
	}

	for _, s := range spares {
		m, ok := listed[s]
		if !ok {
			t.Errorf("%q is a spare the catalog does not hold — two discoveries, not two readings", s)
			continue
		}
		if !m.Base.In.IsZero() || !m.Base.Out.IsZero() {
			t.Errorf("%q is a spare with a price — a downgrade that bills is not a downgrade", s)
		}
	}

	// Widest first, because the request was already sized for the SKU it asked
	// for: a spare that cannot hold the prompt is a second refusal, not a
	// fallback. Then by id, so the answer is a property of the listing and not of
	// the order the vendor happened to serialize it in.
	want := []string{"vendor/big:free", "vendor/small:free"}
	if strings.Join(spares, ",") != strings.Join(want, ",") {
		t.Errorf("spares = %v, want %v", spares, want)
	}
}

// FREE IS NOT THE SAME QUESTION AS USABLE, and the live listing is what taught it:
// the two WIDEST free routes OpenRouter advertises are google/lyria-3-*, which
// declare out:["text","audio"] because they are music models. Ordered by context
// alone they sat at the top of the fallback list, so a chat request refused for
// money would have been handed to a music generator — a wrong answer where an
// honest error belonged, which is worse than the outage it was fixing.
func TestASpareMustAnswerInTextAndNothingElse(t *testing.T) {
	for _, id := range openrouterSpare([]byte(spareListing)) {
		switch id {
		case "vendor/music:free":
			t.Error("a model that also answers in audio is a spare — a chat request would be handed to a music generator")
		case "vendor/silent:free":
			t.Error("a model that declares no output modality is a spare — this is the one place that must not guess")
		}
	}
}

// A vendor whose listing we could not read gets no fallback invented for it.
func TestAnUnreadableListingHasNoSpares(t *testing.T) {
	if got := openrouterSpare([]byte("not json")); len(got) != 0 {
		t.Errorf("spares = %v, want none", got)
	}
	if got := openrouterSpare([]byte(`{"data":[{"id":"  ","pricing":{"prompt":"0","completion":"0"}}]}`)); len(got) != 0 {
		t.Errorf("spares = %v, want none — a nameless SKU is not a route", got)
	}
}

// The one wiring that makes any of this reachable: OpenRouter declares its
// dialect's reading of "free", so the family knows what it still serves when the
// account is empty.
func TestOpenRouterDeclaresItsSpareRoutes(t *testing.T) {
	if openrouterFam.spare == nil {
		t.Fatal("openrouter declares no spare routes — a 402 from it would be final")
	}
}

// Discovery carries the spare routes: ONE fetch, both projections, and the family
// prices a spare at nothing because the vendor does.
func TestDiscoveryCarriesTheSpareRoutes(t *testing.T) {
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("discovery asked for %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(spareListing))
	}))
	defer vendor.Close()

	fam := &modelFamily{
		name: "openrouter", prefix: "openrouter/",
		decode: openrouterCatalog,
		spare:  openrouterSpare,
		providerFn: func() *object.Provider {
			return &object.Provider{Owner: "admin", Name: "openrouter", Type: "OpenRouter", ProviderUrl: vendor.URL}
		},
	}
	if err := fam.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if got := fam.spareRoutes(); len(got) != 2 || got[0] != "vendor/big:free" {
		t.Errorf("spareRoutes = %v", got)
	}
	if !fam.isSpare("vendor/big:free") || !fam.isSpare("VENDOR/BIG:FREE") {
		t.Error("a discovered free route is not recognised as a spare")
	}
	if fam.isSpare("vendor/paid-a") {
		t.Error("a priced SKU reads as a spare — it would bill at nothing")
	}
	// A spare is a catalog entry the vendor prices at nothing, so a caller may name
	// it and a refusal for money may land on it, and both bill the same zero.
	spare, ok := fam.lookup("vendor/big:free")
	if !ok {
		t.Fatal("a free route the vendor advertises is missing from the catalog")
	}
	if !spare.Base.In.IsZero() || !spare.Base.Out.IsZero() {
		t.Error("a spare route carries a price")
	}
	if spare.MinTier != "" || spare.Funding != "" {
		t.Errorf("a free route carries floors (tier=%q funding=%q) — a free plan cannot reach it", spare.MinTier, spare.Funding)
	}
}

// refuses is a family that answers `status`/`body` for anything but `free`, and
// answers `free` normally. It records every SKU it was asked for, which is what
// makes "the fallback did not fire" an assertion rather than an absence.
type refuses struct {
	status int
	body   string
	free   string
	asked  []string
}

func (f *refuses) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string `json:"model"`
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		_ = json.Unmarshal(body, &in)
		f.asked = append(f.asked, in.Model)

		if in.Model == f.free {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"gen-1","model":"` + f.free + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
}

// pipe drives the real relay against that family and returns what the client got.
func (f *refuses) pipe(t *testing.T, fam *modelFamily, sku string) (*httptest.ResponseRecorder, []attempt) {
	t.Helper()
	body := []byte(`{"model":"` + sku + `","messages":[{"role":"user","content":"2+2?"}]}`)
	rec := httptest.NewRecorder()
	ctx := web.NewContext()
	ctx.Reset(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body))))
	c := &ApiController{}
	c.Init(ctx, "ApiController", "X", nil)
	out := c.pipeToFamily(fam, "chat/completions", "openai", sku, body, false, "acme", nil, false, nil, time.Now())
	return rec, out
}

func spareFamily(t *testing.T, url, free string) *modelFamily {
	t.Helper()
	fam := &modelFamily{
		name: "openrouter", prefix: "openrouter/",
		providerFn: func() *object.Provider {
			return &object.Provider{Owner: "admin", Name: "openrouter", Type: "OpenRouter", ProviderUrl: url}
		},
	}
	// Set the snapshot directly: what is under test here is what the pipe DOES
	// with a spare, not how discovery found one (TestDiscoveryCarriesTheSpareRoutes
	// covers that), and refreshing would put a second HTTP round trip inside every
	// case of the table below.
	fam.spares = []string{free}
	fam.loaded = true
	fam.fetchedAt = time.Now()
	return fam
}

// THE DISCRIMINATION, which is the whole design. A vendor that cannot serve at all
// — its account with us spent, or its own failure — moves the request to a free
// route, because a route it charges nothing for is subject to neither. Everything
// else fails as itself: a malformed body, a model this vendor has not got, refused
// content and a rate limit are all facts the caller needs, and answering any of
// them with a smaller model hides a real failure that cannot be debugged later.
// A rate limit is the near miss — it clears in seconds, so it is waited out rather
// than downgraded.
//
// Each case drives the REAL relay against a real HTTP vendor, and the assertion
// is on what the vendor was ASKED — so "it did not fall back" is a fact about the
// wire, not about a branch nobody took.
func TestOnlyAVendorThatCannotServeMovesTheRequest(t *testing.T) {
	const free = "vendor/big:free"
	const sku = "vendor/paid-a"

	// The 402 body is OpenRouter's own, measured from the live account.
	const spent = `{"error":{"message":"Insufficient credits. Add more using https://openrouter.ai/settings/credits","code":402,"metadata":{"limit_source":"openrouter_credits"}}}`

	for _, tc := range []struct {
		name   string
		status int
		body   string
		moves  bool
	}{
		{"out of credit", 402, spent, true},
		{"payment required, bare", 402, `{"error":{"message":"payment required"}}`, true},

		{"malformed request", 400, `{"error":{"message":"messages: invalid role"}}`, false},
		{"model not carried", 404, `{"error":{"message":"No endpoints found for vendor/paid-a."}}`, false},
		{"content refused", 422, `{"error":{"message":"content policy violation"}}`, false},
		{"credential rejected", 401, `{"error":{"message":"unauthorized"}}`, false},
		{"forbidden", 403, `{"error":{"message":"forbidden"}}`, false},
		{"rate limited", 429, `{"error":{"message":"rate limit exceeded"}}`, false},

		{"vendor broken", 500, `{"error":{"message":"internal server error"}}`, true},
		{"bad gateway", 502, `{"error":{"message":"bad gateway"}}`, true},
		{"gateway timeout", 504, `{"error":{"message":"gateway timeout"}}`, true},
		// THE ONE THAT MATTERS MOST. The customer's own paywall, relayed by a
		// service that fronts for us: same status, opposite fact. It is the
		// customer who owes money, and serving them a free model instead would
		// tell them their payment problem had fixed itself. Told apart by the
		// CODE we stamp on every denial we make, which no vendor writes.
		{"our own balance gate", 402, `{"error":{"message":"Insufficient balance. Add credits to your wallet at https://pay.hanzo.ai","type":"billing_error","code":"insufficient_balance"}}`, false},
		{"our own balance lookup blip", 402, `{"error":{"message":"Unable to verify your balance right now.","type":"billing_error","code":"balance_unavailable"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cooled.forget()
			fake := &refuses{status: tc.status, body: tc.body, free: free}
			vendor := fake.serve(t)
			defer vendor.Close()
			fam := spareFamily(t, vendor.URL, free)

			rec, refusedBy := fake.pipe(t, fam, sku)

			moved := len(fake.asked) > 1
			if moved != tc.moves {
				t.Fatalf("asked=%v: moved=%v, want %v", fake.asked, moved, tc.moves)
			}
			if fake.asked[0] != sku {
				t.Errorf("first ask was %q, want the SKU the caller named", fake.asked[0])
			}
			if !tc.moves {
				// The refusal must reach somebody as itself: either relayed to the
				// client, or handed back so another VENDOR can be tried. What it must
				// never be is a quietly different model.
				if refusedBy == nil && rec.Body.Len() == 0 {
					t.Error("the refusal vanished")
				}
				if strings.Contains(rec.Body.String(), free) {
					t.Errorf("a %d was answered with the free route:\n%s", tc.status, rec.Body.String())
				}
				return
			}
			if fake.asked[1] != free {
				t.Errorf("second ask was %q, want the spare route", fake.asked[1])
			}
			if refusedBy != nil {
				t.Errorf("the request was handed on after it had already been served: %v", refusedBy)
			}
		})
	}
}

// The answer wears the model that MADE it. A downgrade the client cannot see is a
// lie about what they got, so the one thing the relay must not do is stamp the
// SKU that was asked for onto an answer a different model wrote.
func TestASpareAnswerSaysWhichModelMadeIt(t *testing.T) {
	cooled.forget()
	const free = "vendor/big:free"
	const sku = "vendor/paid-a"
	fake := &refuses{status: 402, body: `{"error":{"message":"Insufficient credits."}}`, free: free}
	vendor := fake.serve(t)
	defer vendor.Close()

	rec, _ := fake.pipe(t, spareFamily(t, vendor.URL, free), sku)

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, rec.Body.String())
	}
	if got["model"] != free {
		t.Errorf("model = %v, want %q — the client is told which model answered", got["model"], free)
	}
	if got["model"] == sku {
		t.Error("the answer wears the SKU that was asked for, which no model wrote")
	}
}

// A spare bills nothing, because the vendor charges nothing for it — that is the
// whole reason it can answer while the account is empty. Charging retail for a
// downgrade the customer did not choose is taking money for the outage.
func TestASpareRouteIsBilledAtNothing(t *testing.T) {
	fam := &modelFamily{name: "openrouter", spares: []string{"vendor/big:free"}, loaded: true, fetchedAt: time.Now()}
	c := &ApiController{}
	ctx := web.NewContext()
	ctx.Reset(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/x", nil))
	c.Init(ctx, "ApiController", "X", nil)

	if cents := c.recordFamilyUsage(fam, "vendor/big:free", "vendor/paid-a", nil, &mark{}, nil, false, false, "r1", 1000, 1000, time.Now(), nil, "success", ""); cents != 0 {
		t.Errorf("a spare route billed %d cents", cents)
	}

	// And the zero has to hold at every reader of the money, not only in the hold.
	// The DEBIT, the warehouse row and the span each ask usageCostNano, and a price
	// table asked about a SKU it has never seen answers with its conservative
	// default — which is the right guess for an unknown model and exactly the wrong
	// one here, because it bills a customer for the fallback they were handed.
	free := &usageRecord{Model: "vendor/big:free", Free: true, Status: "success", PromptTokens: 1000, CompletionTokens: 1000}
	if got := usageCostNano(free); got != 0 {
		t.Errorf("a free route costs %d nano — the customer would be billed for the outage", got)
	}
	if got := providerCostNano(free); got != 0 {
		t.Errorf("a free route has COGS of %d nano", got)
	}
	if recordUnpriced(free) {
		t.Error("a free route reads as unpriced — its price is zero, which is a price")
	}
	// The same record without the fact is what the table would have charged, so the
	// assertion above is not vacuous.
	guessed := *free
	guessed.Free = false
	if usageCostNano(&guessed) == 0 {
		t.Skip("the price table happens to price this SKU at zero; the guard is untestable here")
	}
}

// The ledger and the span each carry the fallback, and they carry it as data a
// query can filter on rather than as a sentence in a log line.
func TestAFallbackIsVisibleInTheLedgerAndTheSpan(t *testing.T) {
	rec := &usageRecord{
		Model: "vendor/big:free", Requested: "vendor/paid-a",
		Provider: "openrouter", Owner: "acme", Status: "success",
		PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4,
	}

	// The ledger: `model` is what answered, `requested` what was wanted, and
	// `WHERE requested != ''` is the query for every downgraded generation.
	values := cloudUsageValues(rec, time.Now())
	at := -1
	for i, col := range object.CloudUsageColumns {
		if col == "requested" {
			at = i
		}
	}
	if at < 0 {
		t.Fatal("the ledger has no requested column")
	}
	if values[at] != "vendor/paid-a" {
		t.Errorf("ledger requested = %v, want the SKU the caller named", values[at])
	}

	// The span: the two standard model attributes stop agreeing, and one
	// attribute states the cause so a reader does not have to infer it.
	got := map[string]string{}
	for _, a := range buildGenAISpanFields(rec, 0, 0, 0, nil, nil, false).attrs {
		if a.Value.Type() == attribute.STRING {
			got[string(a.Key)] = a.Value.AsString()
		}
	}
	if got[attrGenAIRequestModel] != "vendor/paid-a" || got[attrGenAIResponseModel] != "vendor/big:free" {
		t.Errorf("span models: request=%q response=%q — they must say what was asked and what answered",
			got[attrGenAIRequestModel], got[attrGenAIResponseModel])
	}
	if got[attrFallback] != fallbackSpent {
		t.Errorf("span fallback = %q, want %q", got[attrFallback], fallbackSpent)
	}

	// An ordinary generation carries none of it: absent IS the signal, so the
	// attribute stays a filter rather than something to interpret.
	plain := &usageRecord{Model: "vendor/paid-a", Provider: "openrouter", Status: "success"}
	for _, a := range buildGenAISpanFields(plain, 0, 0, 0, nil, nil, false).attrs {
		if string(a.Key) == attrFallback {
			t.Error("an ordinary generation is tagged as a fallback")
		}
	}
	if v := cloudUsageValues(plain, time.Now())[at]; v != "" {
		t.Errorf("ledger requested = %v on an ordinary row, want empty", v)
	}
}

// A family that declares no spare routes is unchanged: its refusal is final and
// is handed on for another VENDOR to answer, exactly as before.
func TestAFamilyWithNoSpareIsUnchanged(t *testing.T) {
	cooled.forget()
	fake := &refuses{status: 402, body: `{"error":{"message":"Insufficient credits."}}`, free: "unused"}
	vendor := fake.serve(t)
	defer vendor.Close()

	fam := &modelFamily{
		name: "enso", prefix: "enso",
		providerFn: func() *object.Provider {
			return &object.Provider{Owner: "admin", Name: "enso", Type: "Enso", ProviderUrl: vendor.URL}
		},
		loaded: true, fetchedAt: time.Now(),
	}
	_, refusedBy := fake.pipe(t, fam, "enso-4")

	if len(fake.asked) != 1 {
		t.Errorf("asked=%v, want one attempt — this family has nothing to fall back to", fake.asked)
	}
	if len(refusedBy) != 1 || refusedBy[0].status != http.StatusPaymentRequired {
		t.Fatalf("refusal = %v, want the 402 handed on to the next vendor", refusedBy)
	}
	// And it is never relayed to the customer as a 402: exhausted() answers 503,
	// because "we could not serve this" is true and "you owe money" is not.
	if s := upstreamHTTPStatus(exhausted("enso-4", refusedBy)); s != http.StatusServiceUnavailable {
		t.Errorf("every-vendor-refused status = %d, want 503", s)
	}
}

// The vendor's own free router gets no lift: width decides, and it takes its place
// like any other free route.
//
// It always answers, which is a reason to want it first and the wrong one. What it
// answers WITH is not knowable in advance — measured live, one request to it was
// served by a coding model and the next by a content-safety classifier that replied
// "User Safety: safe" to a chat prompt. That is the same hazard the text-only rule
// exists for, and a wrong answer in place of an honest refusal is worse than the
// refusal.
func TestTheVendorsOwnFreeRouterGetsNoLift(t *testing.T) {
	listing := `{"data":[
	 {"id":"vendor/wide:free","context_length":1000000,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]}},
	 {"id":"openrouter/free","context_length":200000,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]}},
	 {"id":"vendor/narrow:free","context_length":8000,"pricing":{"prompt":"0","completion":"0"},"architecture":{"output_modalities":["text"]}}
	]}`
	got := openrouterSpare([]byte(listing))
	want := []string{"vendor/wide:free", "openrouter/free", "vendor/narrow:free"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("spares = %v, want %v", got, want)
	}
}

// A free route is free because the vendor keeps what it carried; a priced one pays
// instead and keeps nothing. One fact, stated to the vendor in its own dialect and
// reported to the caller in ours, so the two can never be different words about the
// same call.
func TestTermsFollowThePrice(t *testing.T) {
	read := func(b []byte) string {
		var m struct {
			Provider struct {
				DataCollection string   `json:"data_collection"`
				Order          []string `json:"order"`
			} `json:"provider"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("body is not JSON after terms: %v", err)
		}
		if m.Model != "vendor/x" {
			t.Errorf("terms rewrote the model to %q", m.Model)
		}
		return m.Provider.DataCollection
	}
	body := []byte(`{"model":"vendor/x","messages":[]}`)
	if got := read(openrouterTerms(body, true)); got != collectionAllow {
		t.Errorf("free route data_collection = %q, want %q", got, collectionAllow)
	}
	if got := read(openrouterTerms(body, false)); got != collectionDeny {
		t.Errorf("priced route data_collection = %q, want %q", got, collectionDeny)
	}

	// A caller's own vendor preferences survive: terms state one field, not the object.
	kept := openrouterTerms([]byte(`{"model":"vendor/x","provider":{"order":["a","b"]}}`), true)
	var m struct {
		Provider struct {
			DataCollection string   `json:"data_collection"`
			Order          []string `json:"order"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(kept, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Provider.Order) != 2 || m.Provider.DataCollection != collectionAllow {
		t.Errorf("a caller's other vendor preferences were dropped: %+v", m.Provider)
	}

	// A body we cannot read goes out as it came in rather than mangled.
	if got := string(openrouterTerms([]byte("not json"), true)); got != "not json" {
		t.Errorf("unreadable body was rewritten to %q", got)
	}
}

// A key embedded in a public page may ask for a route that costs nothing.
//
// The allowlist bounds what such a key can SPEND, so a route the vendor charges
// nothing for is already inside the bound — which is what lets a logged-out page
// be answered with no wallet behind it. An id whose price cannot be read stays out:
// the bound is only kept by refusing what cannot be priced.
func TestAPageKeyMayAskForWhatCostsNothing(t *testing.T) {
	fam := openrouterFam
	saved, savedLoaded, savedAt := fam.byID, fam.loaded, fam.fetchedAt
	t.Cleanup(func() { fam.byID, fam.loaded, fam.fetchedAt = saved, savedLoaded, savedAt })
	fam.byID = map[string]zenModel{
		"vendor/free": {ID: "vendor/free"},
		"vendor/paid": {ID: "vendor/paid", Base: zenTier{In: decimal.New(3, 0), Out: decimal.New(15, 0)}},
	}
	fam.loaded = true
	fam.fetchedAt = time.Now()

	if !widgetMayServe("vendor/free") {
		t.Error("a route priced at zero is refused to a page key")
	}
	if widgetMayServe("vendor/paid") {
		t.Error("a priced route is served to a page key")
	}
	if widgetMayServe("no-such-model-anywhere") {
		t.Error("an unpriceable id is served to a page key — the bound is not kept")
	}
	if !widgetMayServe("llama-3.1-8b") {
		t.Error("an explicitly allowed cheap id is refused")
	}
}
