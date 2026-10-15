// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// ensoAdmin is the enso family's admin catalog as the engine writes it (admin.rs):
// two paid models routed through OpenRouter's credit, one of them with a long
// prompt's price, and a free one.
const ensoAdmin = `{"margin":"3.0","providers":{"openrouter":{"base":"https://openrouter.ai/api/v1","funding":"prepaid"},"free":{"base":"https://openrouter.ai/api/v1"}},
"models":{
 "enso-flash":{"route":[
   {"provider":"openrouter","upstream":"openai/gpt-6-luna","ctx":900000,"cost":{"in":"0.11","out":"0.55","cache_read":"0.011","cache_write":"0.1375"},"long":{"above":272000,"in":"0.2","out":"0.75","cache_read":"0.02","cache_write":"0.25"}},
   {"provider":"openrouter","upstream":"openai/gpt-6-luna-pro","ctx":900000,"cost":{"in":"0.11","out":"0.55","cache_read":"0.011","cache_write":"0.1375"}}]},
 "enso-pro":{"route":[
   {"provider":"openrouter","upstream":"openai/gpt-6.1-sol","ctx":272000,"cost":{"in":"2.20","out":"11.00","cache_read":"0.11","cache_write":"2.75"}},
   {"provider":"openrouter","upstream":"google/gemini-4-argon","ctx":1000000,"cost":{"in":"1.25","out":"10.00","cache_read":"0.125","cache_write":"0.1875"}}]},
 "enso":{"route":[{"provider":"free","upstream":"vendor/x:free","ctx":256000,"cost":{"in":"0","out":"0"}}]},
 "enso-gone":{"disabled":true,"route":[{"provider":"openrouter","upstream":"vendor/y","cost":{"in":"9","out":"9"}}]}}}`

// ensoService is the enso family on httptest: discovery, the admin catalog behind
// its token, and a chat answer that names its arm and states its cost.
type ensoService struct {
	*httptest.Server
	cogs string
}

func ensoServiceAt(t *testing.T) *ensoService {
	t.Helper()
	e := &ensoService{cogs: "0.00066"}
	e.Server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.Close)
	t.Setenv("ENSO_URL", e.URL)
	t.Setenv("ENSO_API_KEY", "")
	t.Setenv("ZEN_ADMIN_TOKEN", zenServiceToken)
	restore(t, ensoFam)
	ensoFam.mu.Lock()
	ensoFam.byID, ensoFam.ids, ensoFam.spares, ensoFam.named, ensoFam.paid = nil, nil, nil, nil, false
	ensoFam.loaded, ensoFam.fetchedAt = false, time.Time{}
	ensoFam.mu.Unlock()
	if err := ensoFam.refresh(); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	prevB, prevL := current.Load(), ladders.Load()
	t.Cleanup(func() {
		current.Store(prevB)
		ladders.Store(prevL)
		ratesAt.Add(1)
	})
	return e
}

func (e *ensoService) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/admin/catalog":
		if r.Header.Get("Authorization") != "Bearer "+zenServiceToken {
			http.Error(w, `{"error":{"message":"forbidden"}}`, http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, ensoAdmin)
	case "/v1/models":
		_, _ = io.WriteString(w, `{"object":"list","paid":true,"data":[
 {"id":"enso-flash","object":"model","owned_by":"hanzo","mode":"chat","context_window":900000,"pricing":{"input":"0.33","output":"1.65"}},
 {"id":"enso-pro","object":"model","owned_by":"hanzo","mode":"chat","context_window":1000000,"pricing":{"input":"3.3","output":"16.5"}},
 {"id":"enso","object":"model","owned_by":"hanzo","mode":"chat","context_window":256000,"pricing":{"input":"0","output":"0"}}]}`)
	case "/v1/chat/completions":
		var in struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(servedHeader, in.Model)
		w.Header().Set(armHeader, "openai/gpt-6-luna")
		w.Header().Set(providerHeader, "openrouter")
		w.Header().Set(cogsHeader, e.cogs)
		fmt.Fprintf(w, `{"id":"g","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":1000,"total_tokens":2000}}`, in.Model)
	default:
		http.NotFound(w, r)
	}
}

// near is a and b equal to a billionth of a dollar.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// orFees are the host's fees as cloud seeds them: OpenRouter's 5.5% on its credit,
// the platform's 5.5% and 1% on a call the org's own key paid for.
func orFees(t *testing.T) {
	fees(t, map[string]string{"openrouter": "5.5", "hanzo": "5.5", "hanzo_byok": "1"})
}

// A fee is a percent of zero or more, and the platform's own is more than zero: a
// read that is no fee keeps the last good one, and a host that states none bills at
// cost.
func TestAFeeIsAPercentAndABadOneKeepsTheLast(t *testing.T) {
	for _, c := range []struct {
		name, raw string
		want      string
		ok        bool
	}{
		{"openrouter", "5.5", "1.055", true},
		{"openrouter", "0", "1", true},
		{"openrouter", "", "1", true},
		{"hanzo", "5.5", "1.055", true},
		{"hanzo", "0", "", false},
		{"hanzo_byok", "-1", "", false},
		{"hanzo", "five", "", false},
	} {
		m, err := feeMultiple(c.name, c.raw)
		if (err == nil) != c.ok || (c.ok && m.Cmp(decimal.MustParse(c.want)) != 0) {
			t.Errorf("feeMultiple(%s, %q) = %s, %v; want %s, ok %v", c.name, c.raw, m.String(), err, c.want, c.ok)
		}
	}
	by := map[string]string{"hanzo": "5.5"}
	object.SetFees(func(n string) string { return by[n] })
	levies.forget()
	t.Cleanup(func() { object.SetFees(nil); levies.forget() })
	if got := platform(false); got.Cmp(decimal.MustParse("1.055")) != 0 {
		t.Fatalf("platform fee = %s, want 1.055", got.String())
	}
	by["hanzo"] = "0"
	levies.mu.Lock()
	levies.got["hanzo"] = feeRead{multiple: levies.got["hanzo"].multiple, at: time.Now().Add(-2 * feeEvery)}
	levies.mu.Unlock()
	levies.read("hanzo")
	if got := platform(false); got.Cmp(decimal.MustParse("1.055")) != 0 {
		t.Errorf("a zero platform fee was taken: %s, want the last good 1.055", got.String())
	}
	if got := feePercent(decimal.MustParse("1.055")); got != "5.5" {
		t.Errorf("feePercent(1.055) = %q, want 5.5", got)
	}
}

// What a call cost us is what was stated and the costliest buying vendor's fee on
// top; its price is that and the platform's fee on top, rounded up to the nano.
func TestACostIsTheVendorsFeeOnTopAndAPriceThePlatformsOnThat(t *testing.T) {
	orFees(t)
	stated := int64(1_000_000)
	if got := *costed(&stated, "openrouter"); got != 1_055_000 {
		t.Errorf("costed through openrouter = %d, want 1055000", got)
	}
	if got := *costed(&stated, "anthropic", "", "free"); got != 1_000_000 {
		t.Errorf("costed through a vendor with no fee = %d, want 1000000", got)
	}
	if got := *costed(&stated, "free", "openrouter"); got != 1_055_000 {
		t.Errorf("a chain any arm of which a reseller bought = %d, want 1055000", got)
	}
	if costed(nil, "openrouter") != nil {
		t.Error("nothing stated costed something")
	}
	if got := plus(1_055_000, false); got != 1_113_025 {
		t.Errorf("plus = %d, want 1113025", got)
	}
	if got := plus(1_000_000, true); got != 1_010_000 {
		t.Errorf("plus byok = %d, want 1010000", got)
	}
	if got := plus(3, false); got != 4 {
		t.Errorf("plus rounds up: %d, want 4", got)
	}
}

// A model resold from OpenRouter bills OpenRouter's price, its fee, and the
// platform's fee on top of both.
func TestAResoldModelBillsTheVendorsFeeAndThePlatformsOnTop(t *testing.T) {
	orFees(t)
	m, err := openrouterMarkup()
	if err != nil {
		t.Fatal(err)
	}
	if want := decimal.MustParse("1.113025"); m.Cmp(want) != 0 {
		t.Errorf("markup = %s, want %s", m.String(), want.String())
	}
}

// A request's depth is read as the family reads it.
func TestADepthIsReadAsTheFamilyReadsIt(t *testing.T) {
	for body, want := range map[string]string{
		`{"model":"m"}`:                                         "default",
		`{"reasoning_effort":"low"}`:                            "low",
		`{"reasoning_effort":"medium"}`:                         "medium",
		`{"reasoning_effort":"xhigh"}`:                          "max",
		`{"reasoning_effort":"minimal"}`:                        "none",
		`{"reasoning":{"effort":"high"}}`:                       "high",
		`{"thinking":{"type":"disabled"}}`:                      "none",
		`{"thinking":{"type":"enabled","budget_tokens":1024}}`:  "low",
		`{"thinking":{"type":"enabled","budget_tokens":8192}}`:  "medium",
		`{"thinking":{"type":"enabled","budget_tokens":32000}}`: "max",
		`not json`: "default",
	} {
		if got := depthOf([]byte(body)); got != want {
			t.Errorf("depthOf(%s) = %q, want %q", body, got, want)
		}
	}
}

// The admin catalog reads into each enabled model's rungs, a long price where the
// rung states one.
func TestTheAdminCatalogReadsIntoRungs(t *testing.T) {
	rs, err := rungsOf([]byte(ensoAdmin))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rs["enso-gone"]; ok {
		t.Error("a disabled model was read")
	}
	flash := rs["enso-flash"]
	if len(flash) != 2 || flash[0].long == nil || flash[0].above != 272000 || flash[1].long != nil {
		t.Fatalf("enso-flash rungs = %+v", flash)
	}
	if flash[0].cost.in.Cmp(decimal.MustParse("0.11")) != 0 || flash[0].long.out.Cmp(decimal.MustParse("0.75")) != 0 {
		t.Errorf("enso-flash's first rung = %+v", flash[0])
	}
}

// From is a model's cheapest arm and up to its dearest, a long prompt's price among
// them, each at what it costs us with the reseller's fee; a rung of no cost is no arm.
func TestFromAndUpToSpanTheArms(t *testing.T) {
	orFees(t)
	rs, _ := rungsOf([]byte(ensoAdmin))
	from, upTo, ok := span(rs["enso-flash"])
	if !ok {
		t.Fatal("no span")
	}
	for name, c := range map[string]struct {
		got  decimal.Decimal
		want string
	}{
		"from in":     {from.in, "0.11605"},
		"from out":    {from.out, "0.58025"},
		"from read":   {from.read, "0.011605"},
		"up to in":    {upTo.in, "0.211"},
		"up to out":   {upTo.out, "0.79125"},
		"up to write": {upTo.write, "0.26375"},
	} {
		if c.got.Cmp(decimal.MustParse(c.want)) != 0 {
			t.Errorf("%s = %s, want %s", name, c.got.String(), c.want)
		}
	}
	from, upTo, _ = span(rs["enso-pro"])
	if from.in.Cmp(decimal.MustParse("1.31875")) != 0 || upTo.out.Cmp(decimal.MustParse("11.605")) != 0 || from.read.Cmp(decimal.MustParse("0.11605")) != 0 {
		t.Errorf("enso-pro span from %+v up to %+v", from, upTo)
	}
	if _, _, ok := span(rs["enso"]); ok {
		t.Error("a free model has an arm that costs")
	}
}

// pricedRows is one model's answers: a short prompt and a long one, both on the
// first arm through OpenRouter, each at the cost its tokens came to.
func pricedRows(now time.Time) []object.PricedRow {
	return []object.PricedRow{
		// 1,000 in at $0.11 and 1,000 out at $0.55, times OpenRouter's 1.055.
		{ID: "a", At: now.Add(-time.Hour), Model: "enso-flash", Reasoning: "low", Served: "openai/gpt-6-luna", Vendor: "openrouter", Status: "success",
			Prompt: 1000, Completion: 1000, CostNano: 696_300, BilledNano: 734_597},
		// Past 272k: 100,000 fresh at $0.2, 200,000 cached at $0.02, 2,000 out at
		// $0.75, times 1.055.
		{ID: "b", At: now.Add(-2 * time.Hour), Model: "enso-flash", Reasoning: "low", Served: "openai/gpt-6-luna", Vendor: "openrouter", Status: "success",
			Prompt: 300_000, Cached: 200_000, Completion: 2000, CostNano: 26_902_500, BilledNano: 28_382_138},
	}
}

// Typical is what a token of the model's calls cost: each call's cost split between
// input and output by its arm's rates, summed, over the tokens.
func TestTypicalIsTheMeasuredCostOfATokenOfTheCalls(t *testing.T) {
	orFees(t)
	rs, _ := rungsOf([]byte(ensoAdmin))
	now := time.Now().UTC()
	rows := pricedRows(now)
	one, w := typical(rows[:1], rs["enso-flash"], costs{})
	if one == nil || w.Requests != 1 {
		t.Fatalf("typical over one call = %+v, %+v", one, w)
	}
	if one.in.Cmp(decimal.MustParse("0.11605")) != 0 || one.out.Cmp(decimal.MustParse("0.58025")) != 0 {
		t.Errorf("one call at the base rates measured %s / %s, want 0.11605 / 0.58025", one.in.String(), one.out.String())
	}
	both, w := typical(rows, rs["enso-flash"], costs{})
	// fresh: 116,050 + 26,902,500 × 20,000/25,500 nano over 101,000 tokens; cached:
	// 26,902,500 × 4,000/25,500 over 200,000; out: 580,250 + 26,902,500 × 1,500/25,500
	// over 3,000.
	in := (116_050.0 + 26_902_500.0*20_000/25_500) / 101_000 / 1000
	read := 26_902_500.0 * 4_000 / 25_500 / 200_000 / 1000
	out := (580_250.0 + 26_902_500.0*1_500/25_500) / 3_000 / 1000
	r := both.rate()
	if !near(r.Input, in) || !near(r.CacheRead, read) || !near(r.Output, out) || r.CacheWrite != 0 || w.Requests != 2 || !w.Since.Equal(rows[1].At) || !w.Until.Equal(rows[0].At) {
		t.Errorf("two calls measured %+v over %+v, want %v / %v / %v", r, w, in, read, out)
	}
	if !near(r.CacheRead, 0.0211) {
		t.Errorf("a cached token at its arm's long read price measured %v, want 0.0211", r.CacheRead)
	}
}

// The board quotes each priced model at each depth measured and at every depth
// together, the platform's fee on its costs; a depth nothing measured quotes from
// and up to and nothing typical; a thin day is measured over the last calls.
func TestTheBoardQuotesEveryPricedModel(t *testing.T) {
	orFees(t)
	ensoServiceAt(t)
	if err := readArms(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rows := pricedRows(now)
	b := &board{computed: now, measured: now, rows: rows, last: rows}
	compute(b, now)
	current.Store(b)
	ratesAt.Add(1)

	q, ok := Price("enso-flash", "low", false, false)
	if !ok || q.Typical == nil || q.Window.Basis != "last" || q.Window.Requests != 2 || q.Fee != "5.5" || len(q.Arms) != 0 {
		t.Fatalf("enso-flash at low = %+v", q)
	}
	if !near(q.From.Input, 0.12243275) || !near(q.UpTo.Input, 0.222605) || !near(q.UpTo.Output, 0.83476875) {
		t.Errorf("enso-flash from %+v up to %+v, want 0.11605 and 0.211 / 0.79125 times 1.055", q.From, q.UpTo)
	}
	if q.Typical.Input < q.From.Input || q.Typical.Input > q.UpTo.Input {
		t.Errorf("typical %v is not between from %v and up to %v", q.Typical.Input, q.From.Input, q.UpTo.Input)
	}
	all, _ := Price("enso-flash", "", false, false)
	if all.Typical == nil || !near(all.Typical.Input, q.Typical.Input) {
		t.Errorf("every depth together = %+v, want the one depth measured", all)
	}
	unmeasured, ok := Price("enso-flash", "max", false, false)
	if !ok || unmeasured.Typical != nil || unmeasured.Reasoning != "max" || !near(unmeasured.From.Input, q.From.Input) {
		t.Errorf("a depth nothing measured = %+v, want from and up to alone", unmeasured)
	}
	byok, _ := Price("enso-flash", "low", true, true)
	if !near(byok.From.Input, 0.11605*1.01) || byok.Fee != "1" || len(byok.Arms) != 2 || byok.Arms[0].Fee != "5.5" || byok.Arms[0].Long == nil {
		t.Errorf("a SuperAdmin's BYOK quote = %+v", byok)
	}
	if pro, ok := Price("enso-pro", "", false, false); !ok || pro.Typical != nil || !near(pro.From.Input, 1.31875*1.055) {
		t.Errorf("enso-pro = %+v", pro)
	}
	if _, ok := Price("enso", "", false, false); ok {
		t.Error("a free model was quoted")
	}
	if _, ok := Price("zen5", "", false, false); ok {
		t.Error("a model not priced at cost was quoted")
	}

	// A day of thin or more calls is measured over the day.
	var day []object.PricedRow
	for i := range thin {
		r := rows[0]
		r.ID = fmt.Sprint("d", i)
		day = append(day, r)
	}
	b = &board{computed: now, measured: now, rows: day, last: rows}
	compute(b, now)
	current.Store(b)
	if q, _ := Price("enso-flash", "low", false, false); q.Window.Basis != "24h" || q.Window.Requests != thin {
		t.Errorf("a full day measured %+v, want the day's %d calls", q.Window, thin)
	}
}

// The ledger counts answers, the arms that failed first, the answers a reseller's
// credit bought, the fee and every loss, by model and vendor.
func TestTheLedgerCountsFallbacksFeesAndLosses(t *testing.T) {
	orFees(t)
	now := time.Now().UTC()
	rows := append(pricedRows(now),
		object.PricedRow{ID: "f", Model: "enso-flash", Served: "openai/gpt-6-luna-pro", Vendor: "openrouter", Status: "failover"},
		object.PricedRow{ID: "z", Model: "enso", Served: "vendor/x:free", Vendor: "free", Status: "success"},
		object.PricedRow{ID: "l", Model: "enso-pro", Served: "openai/gpt-6.1-sol", Vendor: "openrouter", Status: "success", CostNano: 900, BilledNano: 800, LossNano: 100},
	)
	l := books(rows, now)
	if l.Requests != 4 || l.Failovers != 1 || l.Fallbacks != 3 || !near(l.FallbackShare, 0.75) || l.Losses != 1 || l.LossNano != 100 {
		t.Fatalf("ledger = %+v", l)
	}
	if l.FeeNano != (734_597-696_300)+(28_382_138-26_902_500) {
		t.Errorf("fee = %d", l.FeeNano)
	}
	if len(l.Routes) != 3 || l.Routes[1].Model != "enso-flash" || l.Routes[1].Requests != 2 || l.Routes[2].LossNano != 100 {
		t.Errorf("routes = %+v", l.Routes)
	}
}

// An Enso answer bills what it cost us — its family's statement and OpenRouter's fee
// on the credit that bought it — and the platform's fee on top; the row's cost is
// the cost of record. A stand-in capped below its cost is a loss, never silent.
func TestAnEnsoAnswerBillsItsCostAndTheFee(t *testing.T) {
	orFees(t)
	ensoServiceAt(t)
	got := ledger(t)
	alice := &iam.User{Owner: "acme", Name: "alice"}
	body := []byte(`{"model":"enso-flash","reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-flash", body, false, 0, "acme", alice, true, nil, time.Now()); out != nil {
		t.Fatalf("refused: %+v", out)
	}
	_ = drain(t, c)
	ev := got()
	// $0.00066 at the vendor; times 1.055 is $0.0006963, our cost; times 1.055
	// again is $0.0007345965, rounded up to the nano.
	if len(ev) != 1 || ev[0].USD != "0.000734597" || ev[0].CostUSD != "0.0006963" {
		t.Fatalf("the answer debited %+v, want $0.000734597 at a cost of $0.0006963", ev)
	}

	// The same answer standing in for a model priced below what it cost.
	w := whence{ledger: "acme", ip: "10.0.0.1", ctx: context.Background(), asked: ensoFam, depth: "low"}
	h := http.Header{}
	h.Set(armHeader, "openai/gpt-6-luna")
	h.Set(providerHeader, "openrouter")
	h.Set(cogsHeader, "0.66")
	cheap := zenModel{ID: "enso-cheap", Base: zenTier{MaxCtx: 1000, In: decimal.MustParse("0.000001"), Out: decimal.MustParse("0.000001")}}
	cheap.Tiers = []zenTier{cheap.Base}
	ensoFam.mu.Lock()
	ensoFam.byID["enso-cheap"] = cheap
	ensoFam.mu.Unlock()
	nano := recordFamilyUsage(w, ensoFam, "enso-flash", "enso-cheap", nil, &mark{}, alice, true, false, "r", tokens{fresh: 1000, completion: 1000, reported: true}, servingOf(h), time.Now(), nil, "success", "")
	if nano >= 696_300_000 {
		t.Errorf("a stand-in capped at the named model's price billed %d nano", nano)
	}
}

// A call's arms are its row: the one that answered, at the rates it ran at and with
// the tokens it was paid for, then each that failed first and why.
func TestACallsArmsAreItsRow(t *testing.T) {
	orFees(t)
	rs, _ := rungsOf([]byte(ensoAdmin))
	prev := ladders.Load()
	ladders.Store(&rs)
	t.Cleanup(func() { ladders.Store(prev) })
	h := http.Header{}
	h.Set(armHeader, "openai/gpt-6-luna")
	h.Set(providerHeader, "openrouter")
	h.Add(failoverHeader, "openai/gpt-6-luna-pro (openrouter): upstream status 429")
	got := trail("enso-flash", servingOf(h), tokens{fresh: 100_000, cached: 200_000, completion: 10, reasoning: 4})
	var use []armUse
	if err := json.Unmarshal([]byte(got), &use); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if len(use) != 2 || !use[0].Answered || use[0].Rate == nil || use[0].Rate.Input != 0.2 || use[0].Fee != "5.5" ||
		use[0].Cached != 200_000 || use[0].Reasoning != 4 || use[1].Upstream != "openai/gpt-6-luna-pro" || !strings.Contains(use[1].Reason, "429") {
		t.Errorf("arms = %s, want the answer at the long price, then the arm that failed", got)
	}
	if trail("enso-flash", serving{}, tokens{}) != "" {
		t.Error("a call with no arm recorded some")
	}
}

// /v1/models lists a model priced at cost at what its calls typically cost, from and
// up to beside it, marked variable: the same numbers /v1/pricing and the quote read.
func TestTheListingReadsTheBoard(t *testing.T) {
	orFees(t)
	withCatalog(t, "probe-rates")
	ensoServiceAt(t)
	if err := readArms(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rows := pricedRows(now)
	b := &board{computed: now, measured: now, rows: rows, last: rows}
	compute(b, now)
	current.Store(b)
	ratesAt.Add(1)
	q, _ := Price("enso-flash", "", false, false)
	var flash *modelInfo
	models := listAvailableModels()
	for i := range models {
		if models[i].ID == "enso-flash" {
			flash = &models[i]
		}
	}
	if flash == nil || flash.Pricing == nil {
		t.Fatalf("enso-flash not listed priced: %+v", flash)
	}
	p := flash.Pricing
	if !p.Variable || p.From == nil || p.UpTo == nil || p.Fee != "5.5" || p.Window == nil ||
		!near(p.InputPerMillion, q.Typical.Input) || !near(p.From.InputPerMillion, q.From.Input) || !near(p.UpTo.OutputPerMillion, q.UpTo.Output) {
		t.Errorf("enso-flash lists %+v, want the board's %+v", p, q)
	}
	for i := range models {
		if models[i].ID == "enso" && (models[i].Pricing == nil || models[i].Pricing.From != nil || !models[i].Free) {
			t.Errorf("the free model lists %+v", models[i].Pricing)
		}
	}
}

// A blend quotes from its cheapest model and up to its dearest; a model with no
// price is refused.
func TestABlendSpansItsModels(t *testing.T) {
	orFees(t)
	ensoServiceAt(t)
	if err := readArms(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	b := &board{computed: now, measured: now}
	compute(b, now)
	current.Store(b)
	q, err := Blend([]string{"enso-flash", "enso-pro"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !near(q.From.Input, 0.11605*1.055) || !near(q.UpTo.Output, 11.605*1.055) || q.Typical != nil || len(q.Arms) != 2 {
		t.Errorf("blend = %+v", q)
	}
	if _, err := Blend([]string{"enso-flash", "no-such-model"}, false); err == nil {
		t.Error("a blend of an unpriced model was quoted")
	}
	if _, err := Blend(nil, false); err == nil {
		t.Error("an empty blend was quoted")
	}
}

// The family's arms are read under the admin token; a refusal keeps the last read.
func TestArmsAreReadUnderTheAdminToken(t *testing.T) {
	ensoServiceAt(t)
	ladders.Store(nil)
	if err := readArms(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := ladders.Load(); p == nil || len((*p)["enso-pro"]) != 2 {
		t.Fatalf("arms = %v", p)
	}
	t.Setenv("ZEN_ADMIN_TOKEN", "wrong")
	if err := readArms(context.Background()); err == nil {
		t.Error("a refused read succeeded")
	}
	if p := ladders.Load(); p == nil || len((*p)["enso-flash"]) != 2 {
		t.Error("a refused read dropped the arms last read")
	}
}
