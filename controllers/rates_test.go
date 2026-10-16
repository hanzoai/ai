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
	"sync/atomic"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// servedRoute is the enso family's admin catalog as the engine writes it (admin.rs)
// for the catalog it serves (universe charts/app/files/enso/catalog.yaml, 2026-10-15):
// each paid model's arms through OpenRouter's credit, Luna's long prompt's price, and
// a free model on the free lane; a disabled model is not read.
const servedRoute = `{"margin":"3.0","providers":{"openrouter":{"base":"https://openrouter.ai/api/v1","funding":"prepaid"},"free":{"base":"https://openrouter.ai/api/v1"}},
"models":{
 "enso-flash":{"route":[
   {"provider":"openrouter","upstream":"openai/gpt-6-luna","ctx":900000,"cost":{"in":"0.11","out":"0.55","cache_read":"0.011","cache_write":"0.1375"},"long":{"above":272000,"in":"0.2","out":"0.75","cache_read":"0.02","cache_write":"0.25"}},
   {"provider":"openrouter","upstream":"openai/gpt-6-luna-pro","ctx":900000,"cost":{"in":"0.11","out":"0.55","cache_read":"0.011","cache_write":"0.1375"},"long":{"above":272000,"in":"0.2","out":"0.75","cache_read":"0.02","cache_write":"0.25"}}]},
 "enso-pro":{"route":[
   {"provider":"openrouter","upstream":"openai/gpt-6.1-sol","ctx":272000,"cost":{"in":"2.20","out":"11.00","cache_read":"0.11","cache_write":"2.75"}},
   {"provider":"openrouter","upstream":"google/gemini-4-argon","ctx":1000000,"cost":{"in":"2.20","out":"11.00","cache_read":"0.11","cache_write":"0.1875"}},
   {"provider":"openrouter","upstream":"anthropic/claude-sonnet-5.5","ctx":1000000,"cost":{"in":"2.20","out":"11.00","cache_read":"0.11","cache_write":"2.75"}}]},
 "enso-ultra":{"route":[
   {"provider":"openrouter","upstream":"anthropic/claude-sonnet-5.5","ctx":1000000,"cost":{"in":"2.20","out":"11.00","cache_read":"0.11","cache_write":"2.75"}},
   {"provider":"openrouter","upstream":"anthropic/claude-opus-5.5","ctx":1000000,"cost":{"in":"4.40","out":"22.00","cache_read":"0.22","cache_write":"5.50"}},
   {"provider":"openrouter","upstream":"google/gemini-4-argon","ctx":1000000,"cost":{"in":"2.20","out":"11.00","cache_read":"0.11","cache_write":"0.1875"}}]},
 "enso-free":{"route":[{"provider":"free","upstream":"vendor/x:free","ctx":256000,"cost":{"in":"0","out":"0"}}]},
 "enso-gone":{"disabled":true,"route":[{"provider":"openrouter","upstream":"vendor/y","cost":{"in":"9","out":"9"}}]}}}`

// ensoService is the enso family on httptest: discovery, the admin catalog behind
// its token, and a chat answer that names its arm and states its cost.
type ensoService struct {
	*httptest.Server
	route string
	cogs  string
	arm   string
	asked atomic.Int64
}

func ensoServiceAt(t *testing.T) *ensoService {
	t.Helper()
	e := &ensoService{route: servedRoute, cogs: "0.00066", arm: "openai/gpt-6-luna"}
	e.Server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.Close)
	t.Setenv("ENSO_URL", e.URL)
	t.Setenv("ENSO_API_KEY", "")
	t.Setenv("ZEN_ADMIN_TOKEN", zenServiceToken)
	restore(t, ensoFam)
	unread(t, ensoFam)
	ensoFam.mu.Lock()
	ensoFam.byID, ensoFam.ids, ensoFam.spares, ensoFam.named, ensoFam.paid = nil, nil, nil, nil, false
	ensoFam.loaded, ensoFam.fetchedAt = false, time.Time{}
	ensoFam.mu.Unlock()
	if err := ensoFam.refresh(); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	return e
}

func (e *ensoService) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/admin/catalog":
		if r.Header.Get("Authorization") != "Bearer "+zenServiceToken {
			http.Error(w, `{"error":{"message":"forbidden"}}`, http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, e.route)
	case "/v1/models":
		// The service's own quotes, which the family's prices replace; and a model it
		// charges for that no price here names.
		_, _ = io.WriteString(w, `{"object":"list","paid":true,"data":[
 {"id":"enso-flash","object":"model","owned_by":"hanzo","mode":"chat","context_window":900000,"pricing":{"input":"0.11","output":"0.55"}},
 {"id":"enso-pro","object":"model","owned_by":"hanzo","mode":"chat","context_window":1000000,"pricing":{"input":"3.3","output":"16.5"}},
 {"id":"enso-ultra","object":"model","owned_by":"hanzo","mode":"chat","context_window":1000000,"pricing":{"input":"5.236","output":"26.18"}},
 {"id":"enso-rogue","object":"model","owned_by":"hanzo","mode":"chat","context_window":1000000,"pricing":{"input":"9","output":"9"}},
 {"id":"enso-free","object":"model","owned_by":"hanzo","mode":"chat","context_window":256000,"pricing":{"input":"0","output":"0"}}]}`)
	case "/v1/chat/completions":
		e.asked.Add(1)
		var in struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(servedHeader, in.Model)
		w.Header().Set(armHeader, e.arm)
		w.Header().Set(providerHeader, "openrouter")
		w.Header().Set(cogsHeader, e.cogs)
		fmt.Fprintf(w, `{"id":"g","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":1000,"total_tokens":2000}}`, in.Model)
	default:
		http.NotFound(w, r)
	}
}

// unread forgets fam's route as read, for the test and after it, once a read
// running beside a request has ended: a read that ends later would leave what it
// read, or its failure, to the next test.
func unread(t *testing.T, fam *modelFamily) {
	t.Helper()
	forget := func() {
		l := &fam.ladder
		for deadline := time.Now().Add(routeWait); ; time.Sleep(time.Millisecond) {
			l.mu.Lock()
			if !l.busy || time.Now().After(deadline) {
				break
			}
			l.mu.Unlock()
		}
		defer l.mu.Unlock()
		l.rungs, l.at, l.tried, l.err = nil, time.Time{}, time.Time{}, nil
	}
	forget()
	t.Cleanup(forget)
}

// laid states fam's route as read: each model on one arm that costs nothing, so the
// guard sends every one of them whatever it is priced at. For a test of what the pipe
// does with an answer, not of the guard.
func laid(t *testing.T, fam *modelFamily, models ...string) {
	t.Helper()
	unread(t, fam)
	rs := map[string][]rung{}
	for _, m := range models {
		rs[strings.ToLower(m)] = []rung{{provider: "free", upstream: "vendor/" + m + ":free"}}
	}
	fam.ladder.mu.Lock()
	fam.ladder.rungs, fam.ladder.at = rs, time.Now()
	fam.ladder.mu.Unlock()
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
// top; a price at cost is that and the platform's fee on top, rounded up to the nano.
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

// Each Enso model's price is its typical arm's list price times 2 to 3, fixed, and at
// most 60% of Claude Fable's and GPT-6 Astra's ($10 / $50).
func TestEnsoPricesAreAMultipleOfTheirTypicalArm(t *testing.T) {
	want := map[string][4]string{
		"enso-flash": {"0.33", "1.65", "0.033", "0.4125"},
		"enso-pro":   {"4.4", "22", "0.22", "5.5"},
		"enso-ultra": {"5.5", "27.5", "0.275", "6.875"},
	}
	if len(ensoPrices) != len(want) {
		t.Fatalf("%d models priced, want %d", len(ensoPrices), len(want))
	}
	two, three := decimal.New(2, 0), decimal.New(3, 0)
	frontier := dollars("10", "50", "0", "0")
	for id, w := range want {
		p, ok := ensoPrices[id]
		if !ok {
			t.Fatalf("%s has no price", id)
		}
		c := p.price()
		for i, got := range []decimal.Decimal{c.in, c.out, c.read, c.write} {
			if got.Cmp(decimal.MustParse(w[i])) != 0 {
				t.Errorf("%s class %d = %s, want %s", id, i, got.String(), w[i])
			}
		}
		if p.multiple.Cmp(two) < 0 || p.multiple.Cmp(three) > 0 {
			t.Errorf("%s sells at %s times its typical arm, outside 2 to 3", id, p.multiple.String())
		}
		if c.in.Mul(decimal.New(10, 0)).Cmp(frontier.in.Mul(decimal.New(6, 0))) > 0 || c.out.Mul(decimal.New(10, 0)).Cmp(frontier.out.Mul(decimal.New(6, 0))) > 0 {
			t.Errorf("%s at %s / %s is not well under Fable's and Astra's $10 / $50", id, c.in.String(), c.out.String())
		}
	}
}

// Every arm of the catalog the family serves fits under its model's price, at a long
// prompt's price and with OpenRouter's fee: the guard sends every paid model.
func TestTheServedRoutesFitUnderTheirPrices(t *testing.T) {
	orFees(t)
	ensoServiceAt(t)
	rs, err := rungsOf([]byte(servedRoute))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"enso-flash", "enso-pro", "enso-ultra", "enso-free"} {
		m, ok := ensoFam.lookup(id)
		if !ok {
			t.Fatalf("%s not listed", id)
		}
		if why := misfit(m, rs[id]); why != "" {
			t.Errorf("%s: %s", id, why)
		}
	}
	// Twice Sonnet would not hold Opus, OpenRouter's fee on it.
	ultra, _ := ensoFam.lookup("enso-ultra")
	ultra.Base.In, ultra.Base.Out = decimal.MustParse("4.4"), decimal.MustParse("22")
	if why := misfit(ultra, rs["enso-ultra"]); !strings.Contains(why, "claude-opus-5.5") || !strings.Contains(why, "input") {
		t.Errorf("enso-ultra at twice Sonnet: %q, want Opus over it in input", why)
	}
}

// The family's listing carries its own price: a model priced here lists and bills
// at it whatever its service quotes, a free model stays free, and a model the service
// charges for that has no price here is neither listed nor served.
func TestTheListingCarriesTheFixedPrice(t *testing.T) {
	withCatalog(t, "probe-rates")
	ensoServiceAt(t)
	flash, ok := ensoFam.lookup("enso-flash")
	if !ok || flash.Base.In.Cmp(decimal.MustParse("0.33")) != 0 || flash.Base.Out.Cmp(decimal.MustParse("1.65")) != 0 ||
		flash.Base.CacheRead.Cmp(decimal.MustParse("0.033")) != 0 || len(flash.Tiers) != 1 || flash.Base.MaxCtx != 900000 {
		t.Fatalf("enso-flash = %+v, want its fixed price over the service's 0.11 / 0.55", flash)
	}
	if _, ok := ensoFam.lookup("enso-rogue"); ok {
		t.Error("a model with no price here was listed")
	}
	if free, ok := ensoFam.lookup("enso-free"); !ok || free.priced() {
		t.Errorf("the free model = %+v, %v", free, ok)
	}
	var listed *modelInfo
	models := listAvailableModels()
	for i := range models {
		switch models[i].ID {
		case "enso-pro":
			listed = &models[i]
		case "enso-rogue":
			t.Error("/v1/models lists a model with no price")
		case "enso-free":
			if p := models[i].Pricing; p == nil || p.InputPerMillion != 0 || !models[i].Free {
				t.Errorf("the free model lists %+v", p)
			}
		}
	}
	if listed == nil || listed.Pricing == nil || listed.Pricing.Variable || listed.Pricing.InputPerMillion != 4.4 || listed.Pricing.OutputPerMillion != 22 {
		t.Fatalf("enso-pro lists %+v, want 4.4 / 22, fixed", listed)
	}
	if p, ok := ensoFam.modelPrice("enso-ultra"); !ok || p.InputPerMillion != 5.5 || p.OutputPerMillion != 27.5 {
		t.Errorf("enso-ultra holds at %+v, want its price", p)
	}
}

// An Enso answer bills its fixed price for its tokens, whatever its arm cost; the
// row's cost is the cost of record, and the margin is the difference.
func TestAnEnsoAnswerBillsItsFixedPrice(t *testing.T) {
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
	// 1,000 in at $0.33 and 1,000 out at $1.65 a million: $0.00198. It cost $0.00066
	// at the vendor, times OpenRouter's 1.055: $0.0006963.
	if len(ev) != 1 || ev[0].USD != "0.00198" || ev[0].CostUSD != "0.0006963" {
		t.Fatalf("the answer debited %+v, want $0.00198 at a cost of $0.0006963", ev)
	}

	// The same answer standing in for a model priced below what it cost is a loss.
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

// The guard sends nothing to a model whose route costs more than it bills, nor to one
// whose route it cannot check, and names no upstream in its refusal; a model that fits
// is sent.
func TestTheGuardSendsNothingItCannotCheckOrThatCostsMore(t *testing.T) {
	orFees(t)
	e := ensoServiceAt(t)
	e.route = strings.Replace(servedRoute, `"upstream":"google/gemini-4-argon","ctx":1000000,"cost":{"in":"2.20","out":"11.00"`, `"upstream":"google/gemini-4-argon","ctx":1000000,"cost":{"in":"2.20","out":"40.00"`, -1)
	alice := &iam.User{Owner: "acme", Name: "alice"}
	ask := func(model string) (int, string) {
		t.Helper()
		body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody(body)
		if out := c.pipeToFamily(ensoFam, "chat/completions", "openai", model, body, false, 0, "acme", alice, true, nil, time.Now()); out != nil {
			t.Fatalf("%s handed on: %+v", model, out)
		}
		return c.Fiber().Response().StatusCode(), drain(t, c)
	}
	// Gemini's output at $40 is past enso-pro's $22 and enso-ultra's $27.50.
	for _, m := range []string{"enso-pro", "enso-ultra"} {
		status, body := ask(m)
		if status != http.StatusServiceUnavailable || !strings.Contains(body, "costs more than it bills") || strings.Contains(body, "gemini") {
			t.Errorf("%s answered %d %s, want 503 naming no upstream", m, status, body)
		}
	}
	if e.asked.Load() != 0 {
		t.Fatalf("the service was sent %d requests the guard refused", e.asked.Load())
	}
	if status, _ := ask("enso-flash"); status != http.StatusOK || e.asked.Load() != 1 {
		t.Errorf("enso-flash, which fits, answered %d after %d sends", status, e.asked.Load())
	}
	if err := ensoFam.guard(context.Background(), "enso-nothing"); err == nil {
		t.Error("a model with no price was sent")
	}

	// A route it cannot read is no route.
	unread(t, ensoFam)
	t.Setenv("ZEN_ADMIN_TOKEN", "wrong")
	if err := ensoFam.guard(context.Background(), "enso-flash"); err == nil || !strings.Contains(err.Error(), "cannot be checked") {
		t.Errorf("an unreadable route sent: %v", err)
	}
	// Nor is a route that names no arm for the model.
	laid(t, ensoFam, "enso-pro")
	if err := ensoFam.guard(context.Background(), "enso-flash"); err == nil {
		t.Error("a model its route holds no arm for was sent")
	}
}

// The route is read once and kept; a read past routeEvery is made beside the
// request, and one that fails keeps the last; a first read that fails is not asked
// again for routeRetry.
func TestTheRouteIsReadUnderTheAdminTokenAndKept(t *testing.T) {
	ensoServiceAt(t)
	rs, err := ensoFam.route(context.Background())
	if err != nil || len(rs["enso-pro"]) != 3 {
		t.Fatalf("route = %v, %v", rs, err)
	}
	if _, ok := rs["enso-gone"]; ok {
		t.Error("a disabled model was read")
	}
	flash := rs["enso-flash"]
	if flash[0].long == nil || flash[0].above != 272000 || flash[0].long.out.Cmp(decimal.MustParse("0.75")) != 0 {
		t.Errorf("enso-flash's first rung = %+v", flash[0])
	}
	t.Setenv("ZEN_ADMIN_TOKEN", "wrong")
	ensoFam.ladder.stale()
	if _, err := ensoFam.route(context.Background()); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		ensoFam.ladder.mu.Lock()
		busy, failed := ensoFam.ladder.busy, ensoFam.ladder.err
		ensoFam.ladder.mu.Unlock()
		if !busy && failed != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the read beside the request never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rs, _ := ensoFam.route(context.Background()); len(rs["enso-flash"]) != 2 {
		t.Error("a refused read dropped the route last read")
	}
	unread(t, ensoFam)
	if _, err := ensoFam.route(context.Background()); err == nil {
		t.Fatal("a refused first read answered a route")
	}
	t.Setenv("ZEN_ADMIN_TOKEN", zenServiceToken)
	if _, err := ensoFam.route(context.Background()); err == nil {
		t.Error("a first read that failed was asked again before routeRetry")
	}
}

// A router id lists beside its tiers at the price of the tier a request stating no
// depth is served at, never free: only enso-free is.
func TestARouterIdListsAtItsDefaultTiersPrice(t *testing.T) {
	depthConfig(t)
	ensoServiceAt(t)
	rows := map[string]modelInfo{}
	for _, m := range listAvailableModels() {
		rows[m.ID] = m
	}
	for _, id := range []string{"enso", "enso-auto"} {
		m, ok := rows[id]
		if !ok {
			t.Fatalf("%s is not listed", id)
		}
		if p := m.Pricing; p == nil || m.Free || p.InputPerMillion != 0.33 || p.OutputPerMillion != 1.65 || m.Class != object.ClassOurs || m.Family != "enso" {
			t.Errorf("%s lists %+v at %+v, want enso-flash's 0.33 / 1.65, ours, enso", id, m, p)
		}
		if m.ContextWindow != 900000 {
			t.Errorf("%s holds %d, want enso-flash's 900000, the narrowest of its tiers", id, m.ContextWindow)
		}
	}
	if m := rows["enso-free"]; !m.Free {
		t.Errorf("enso-free lists %+v, want free", m)
	}
}

// The price list states each model's price, and the free models; a SuperAdmin reads
// how each price was set, its route held to it, and the ledger.
func TestThePriceListStatesOneNumberAModel(t *testing.T) {
	orFees(t)
	ensoServiceAt(t)
	b := Prices(context.Background(), false)
	if len(b.Prices) != 3 || b.Prices[0].Model != "enso-flash" || b.Prices[0].Price.Input != 0.33 || b.Prices[2].Price.Output != 27.5 ||
		b.Prices[1].Typical != "" || b.Prices[1].Arms != nil || b.Ledger != nil {
		t.Fatalf("the price list = %+v", b)
	}
	if len(b.Free) != 1 || b.Free[0] != "enso-free" {
		t.Errorf("free = %v, want [enso-free]", b.Free)
	}
	admin := Prices(context.Background(), true)
	ultra := admin.Prices[2]
	if ultra.Typical != "anthropic/claude-sonnet-5.5" || ultra.Multiple != "2.5" || ultra.List == nil || ultra.List.Input != 2.2 ||
		len(ultra.Arms) != 3 || ultra.Arms[1].Over != nil || ultra.Arms[1].Fee != "5.5" || ultra.Arms[1].Kind != reseller || ultra.Refused != "" {
		t.Errorf("a SuperAdmin's enso-ultra = %+v", ultra)
	}
	if !object.DatastoreEnabled() && (admin.Ledger != nil || admin.Error == "") {
		t.Errorf("a ledger with no datastore = %+v, error %q", admin.Ledger, admin.Error)
	}
}

// The ledger reports what each model routed to: answers and failovers by arm, what
// they cost and billed, the margin over the answers whose cost is known, the share
// OpenRouter's credit bought, and every loss.
func TestTheLedgerReportsEachModelsArmsAndMargin(t *testing.T) {
	orFees(t)
	now := time.Now().UTC()
	l := books([]object.Routed{
		{
			Model: "enso-flash", Served: "openai/gpt-6-luna", Vendor: "openrouter", Status: "success", Requests: 3, Uncosted: 1,
			CostNano: 1_392_600, BilledNano: 5_940_000, KnownNano: 3_960_000,
		},
		{Model: "enso-flash", Served: "openai/gpt-6-luna-pro", Vendor: "openrouter", Status: "failover", Requests: 2},
		{Model: "enso", Served: "vendor/x:free", Vendor: "free", Status: "success", Requests: 4},
		{
			Model: "enso-pro", Served: "openai/gpt-6.1-sol", Vendor: "openrouter", Status: "success", Requests: 1, Losses: 1,
			CostNano: 900, BilledNano: 800, KnownNano: 800, LossNano: 100,
		},
		{Model: "enso-pro", Served: "openai/gpt-6.1-sol", Vendor: "openrouter", Status: "error", Requests: 9},
	}, now)
	if l.Requests != 8 || l.Failovers != 2 || l.Fallbacks != 4 || !near(l.FallbackShare, 0.5) || l.Uncosted != 1 || l.Losses != 1 || l.LossNano != 100 {
		t.Fatalf("ledger = %+v", l)
	}
	if l.MarginNano != (3_960_000-1_392_600)+(800-900) || l.CostNano != 1_393_500 || l.BilledNano != 5_940_800 {
		t.Errorf("margin %d, cost %d, billed %d", l.MarginNano, l.CostNano, l.BilledNano)
	}
	if len(l.Routes) != 4 || l.Routes[1].Served != "openai/gpt-6-luna" || l.Routes[1].Requests != 3 || l.Routes[2].Failovers != 2 || l.Routes[3].MarginNano != -100 {
		t.Errorf("routes = %+v", l.Routes)
	}
}

// A call's arms are its row: the one that answered, at the rates it ran at and with
// the tokens it was paid for, then each that failed first and why. An answer on an
// arm the route as read does not hold sends the guard to read the route again; one on
// an arm that fits does not.
func TestACallsArmsAreItsRow(t *testing.T) {
	orFees(t)
	ensoServiceAt(t)
	if _, err := ensoFam.route(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set(armHeader, "openai/gpt-6-luna")
	h.Set(providerHeader, "openrouter")
	h.Add(failoverHeader, "openai/gpt-6-luna-pro (openrouter): upstream status 429")
	got := trail(ensoFam, "enso-flash", servingOf(h), tokens{fresh: 100_000, cached: 200_000, completion: 10, reasoning: 4})
	var use []armUse
	if err := json.Unmarshal([]byte(got), &use); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if len(use) != 2 || !use[0].Answered || use[0].Rate == nil || use[0].Rate.Input != 0.2 || use[0].Fee != "5.5" ||
		use[0].Cached != 200_000 || use[0].Reasoning != 4 || use[1].Upstream != "openai/gpt-6-luna-pro" || !strings.Contains(use[1].Reason, "429") {
		t.Errorf("arms = %s, want the answer at the long price, then the arm that failed", got)
	}
	read := func() time.Time {
		ensoFam.ladder.mu.Lock()
		defer ensoFam.ladder.mu.Unlock()
		return ensoFam.ladder.at
	}
	ensoFam.answered("enso-flash", servingOf(h))
	if read().IsZero() {
		t.Fatal("an answer on an arm that fits sent the guard to read the route again")
	}
	h.Set(armHeader, "vendor/elsewhere")
	ensoFam.answered("enso-flash", servingOf(h))
	if !read().IsZero() {
		t.Error("an answer on an arm the route does not hold left the route as read")
	}
	if trail(ensoFam, "enso-flash", serving{}, tokens{}) != "" {
		t.Error("a call with no arm recorded some")
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
