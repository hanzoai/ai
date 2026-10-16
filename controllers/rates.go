// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

// rates.go — what an Enso model costs: one fixed price a model, and what its calls
// cost us.
//
// An Enso model is a route over arms: the family's catalog names each upstream, the
// provider whose account runs it and what a million of its tokens cost. It is sold
// at ONE price, set here (ensoPrices): 2 to 3 times the list price of the upstream
// it typically routes to, and well under Claude Fable and GPT-6 Astra. The price does
// not move with routing, depth or what calls measured. The family's listing carries
// it (fix), so /v1/models lists it, a call bills it (zenModel.bill), a hold reserves
// it, and the price list reads it here (Prices): one number wherever it is read.
//
// Two things hold the price to what the arms cost:
//
//	the guard   no request goes to a model whose route holds an arm that costs us
//	            more than the model bills, in any token class, at a long prompt's
//	            price and with a reseller's fee (guard). The route is the family's
//	            admin catalog, read at most once a routeEvery.
//	the ledger  every call's row states what it cost us, what it billed and the arm
//	            that answered (trail); Prices reports the last day by model and arm,
//	            with the margin the price made. A report: nothing reads it to price.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// ensoPrices is what a million tokens of each Enso model the family charges for
// bill: the list price of the upstream its route typically answers on (the catalog's
// cost of its first rung, universe charts/app/files/enso/catalog.yaml) times a
// multiple of 2 to 3. A model the catalog serves at no charge has no line and bills
// nothing. Claude Fable 5.1 and GPT-6 Astra list at $10 / $50; the dearest here is
// 55% of either. Owner, 2026-10-15.
var ensoPrices = map[string]tag{
	// GPT-6 Luna, three times: past a 272k-token prompt Luna costs $0.20 / $0.75 /
	// $0.02 / $0.25, which twice its short price would cover by 4% of input once
	// OpenRouter's fee is on it.
	"enso-flash": {typical: "openai/gpt-6-luna", list: dollars("0.11", "0.55", "0.011", "0.1375"), multiple: decimal.New(3, 0)},
	// GPT-6.1 Sol, twice. Every arm of the route (Sol, Gemini 4 Argon, Claude Sonnet
	// 5.5) costs Sol's price.
	"enso-pro": {typical: "openai/gpt-6.1-sol", list: dollars("2.20", "11.00", "0.11", "2.75"), multiple: decimal.New(2, 0)},
	// Claude Sonnet 5.5, which answers most of its calls, two and a half times: the
	// Claude Opus 5.5 arm behind it costs twice Sonnet, so twice Sonnet would leave
	// that arm, with OpenRouter's fee, above the price.
	"enso-ultra": {typical: "anthropic/claude-sonnet-5.5", list: dollars("2.20", "11.00", "0.11", "2.75"), multiple: decimal.MustParse("2.5")},
}

// tag is how one model's price was set: the upstream it typically routes to, that
// upstream's list price, and the multiple of it the model sells at.
type tag struct {
	typical  string
	list     costs
	multiple decimal.Decimal
}

// price is what a million of the model's tokens bill, class by class.
func (p tag) price() costs { return p.list.times(p.multiple) }

// dollars is a list price a million tokens, from its four classes' decimals.
func dollars(in, out, read, write string) costs {
	return costs{in: decimal.MustParse(in), out: decimal.MustParse(out), read: decimal.MustParse(read), write: decimal.MustParse(write)}
}

// fix lays the family's own prices over its service's listing: a model priced here
// lists and bills at that price whatever its service quotes; a model its service
// serves at no charge stays free; a model its service charges for that is not priced
// here is dropped, since nothing here may bill it. A family with no prices of its own
// is listed as its service lists it.
func (f *modelFamily) fix(models []zenModel) []zenModel {
	if f.prices == nil {
		return models
	}
	out := models[:0]
	for _, m := range models {
		if p, ok := f.prices[strings.ToLower(m.ID)]; ok {
			c := p.price()
			m.Base = zenTier{MaxCtx: m.MaxCtx, In: c.in, Out: c.out, CacheRead: c.read, CacheWrite: c.write}
			m.Tiers = []zenTier{m.Base}
			out = append(out, m)
			continue
		}
		if !m.priced() {
			out = append(out, m)
			continue
		}
		log.Error("%s: %s is priced by its service and has no price here: it is neither listed nor served", f.name, m.ID)
	}
	return out
}

// Rate is a price in US dollars a million tokens.
type Rate struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// Listing is one Enso model's price.
type Listing struct {
	Model string `json:"model"`
	Price Rate   `json:"price"`
	// Typical, List and Multiple say how the price was set: the upstream the model
	// typically routes to, that upstream's list price, and the multiple of it the
	// price is. They, Arms and Refused name upstreams: a SuperAdmin's read alone
	// carries them.
	Typical  string `json:"typical,omitempty"`
	List     *Rate  `json:"list,omitempty"`
	Multiple string `json:"multiple,omitempty"`
	// Arms is the model's route as last read.
	Arms []Arm `json:"arms,omitempty"`
	// Refused is why the guard sends the model nothing, "" while it is served.
	Refused string `json:"refused,omitempty"`
}

// Arm is one rung of a model's route: its upstream, the provider whose account runs
// it, and the provider's price.
type Arm struct {
	Upstream string `json:"upstream"`
	Provider string `json:"provider"`
	// Kind is the path the provider is (resolve.go): direct, family, do or
	// openrouter.
	Kind string `json:"kind"`
	// Fee is the reseller's fee on the provider's price, percent; "0" for none.
	Fee  string `json:"fee"`
	List Rate   `json:"list"`
	// Long is the provider's price for a prompt past Above tokens.
	Long  *Rate `json:"long,omitempty"`
	Above int   `json:"above,omitempty"`
	// Over names the token classes in which the arm costs us more than the model
	// bills; empty when it fits under the price.
	Over []string `json:"over,omitempty"`
}

// Route is one model's calls on one arm through one provider's account over the
// day, nano-dollars.
type Route struct {
	Model  string `json:"model"`
	Served string `json:"served"`
	Vendor string `json:"vendor"`
	// Requests are the answers the arm gave; Failovers the times it failed first.
	Requests  int `json:"requests"`
	Failovers int `json:"failovers"`
	// Uncosted are answers that stated no cost, which the margin leaves out.
	Uncosted   int   `json:"uncosted"`
	CostNano   int64 `json:"cost_nano"`
	BilledNano int64 `json:"billed_nano"`
	// MarginNano is what the answers whose cost is known billed past their cost.
	MarginNano int64 `json:"margin_nano"`
	LossNano   int64 `json:"loss_nano"`
}

// Ledger is what the family's calls did over the last day: in all, and by model and
// arm.
type Ledger struct {
	Since    time.Time `json:"since"`
	Until    time.Time `json:"until"`
	Requests int       `json:"requests"`
	// Failovers are arms that failed before an answer.
	Failovers int `json:"failovers"`
	// Fallbacks are answers a reseller's credit bought (OpenRouter, the path a call
	// takes when no provider key of its own serves it); FallbackShare their share of
	// the answers.
	Fallbacks     int     `json:"fallbacks"`
	FallbackShare float64 `json:"fallback_share"`
	Uncosted      int     `json:"uncosted"`
	// Losses are answers that cost us more than they billed: each is a row
	// (loss_nano), and the guard keeps there being none.
	Losses     int     `json:"losses"`
	LossNano   int64   `json:"loss_nano"`
	CostNano   int64   `json:"cost_nano"`
	BilledNano int64   `json:"billed_nano"`
	MarginNano int64   `json:"margin_nano"`
	Routes     []Route `json:"routes"`
}

// Board is Enso's prices, and to a SuperAdmin what its calls did.
type Board struct {
	Prices []Listing `json:"prices"`
	// Free names the family's models served at no charge.
	Free []string `json:"free,omitempty"`
	// Ledger is the last day's, a SuperAdmin's read alone.
	Ledger *Ledger `json:"ledger,omitempty"`
	// Error is why the ledger could not be read, "" when it was.
	Error string `json:"error,omitempty"`
}

// Prices is every Enso model's price and the models served free. A SuperAdmin's read
// (admin) also says how each price was set, each model's route as last read and
// whether the guard serves it, and what the family's calls did over the last day.
func Prices(ctx context.Context, admin bool) Board {
	f := ensoFam
	f.warm()
	b := Board{Prices: make([]Listing, 0, len(f.prices))}
	var rs map[string][]rung
	var rerr error
	if admin {
		rs, rerr = f.route(ctx)
	}
	for _, id := range slices.Sorted(maps.Keys(f.prices)) {
		p := f.prices[id]
		l := Listing{Model: id, Price: p.price().rate()}
		if admin {
			list := p.list.rate()
			l.Typical, l.List, l.Multiple = p.typical, &list, p.multiple.String()
			for _, r := range rs[id] {
				l.Arms = append(l.Arms, r.arm(p.price()))
			}
			switch m, ok := f.lookup(id); {
			case rerr != nil:
				l.Refused = "its route cannot be read: " + rerr.Error()
			case !ok:
				l.Refused = "its service does not list it"
			default:
				l.Refused = misfit(m, rs[id])
			}
		}
		b.Prices = append(b.Prices, l)
	}
	for _, m := range f.snapshot() {
		if !m.priced() {
			b.Free = append(b.Free, m.ID)
		}
	}
	slices.Sort(b.Free)
	if admin {
		l, err := reckon(ctx, f, time.Now().UTC())
		if err != nil {
			b.Error = err.Error()
		} else {
			b.Ledger = &l
		}
	}
	return b
}

// costs is a price per million tokens.
type costs struct{ in, out, read, write decimal.Decimal }

// readRate is a cached token's rate: the stated one, else input's.
func (c costs) readRate() decimal.Decimal {
	if c.read.Sign() > 0 {
		return c.read
	}
	return c.in
}

// writeRate is a written token's rate: the stated one, else input's.
func (c costs) writeRate() decimal.Decimal {
	if c.write.Sign() > 0 {
		return c.write
	}
	return c.in
}

func (c costs) times(m decimal.Decimal) costs {
	return costs{c.in.Mul(m), c.out.Mul(m), c.read.Mul(m), c.write.Mul(m)}
}

func (c costs) rate() Rate {
	f := func(d decimal.Decimal) float64 { v, _ := strconv.ParseFloat(d.String(), 64); return v }
	return Rate{Input: f(c.in), Output: f(c.out), CacheRead: f(c.read), CacheWrite: f(c.write)}
}

// rung is one arm as the family's catalog states it: its provider's price, and the
// price past a long prompt where the provider charges one.
type rung struct {
	provider, upstream string
	cost               costs
	long               *costs
	above              int64
}

// paid is what a token of the rung costs us: its price, the reseller's fee on top.
func (r rung) paid(prompt int64) costs {
	c := r.cost
	if r.long != nil && prompt > r.above {
		c = *r.long
	}
	return c.times(levies.multiple(r.provider))
}

// over names the token classes in which the rung costs us more than price bills, at
// its own price and at a long prompt's where it charges one.
func (r rung) over(price costs) []string {
	ends := []costs{r.paid(0)}
	if r.long != nil {
		ends = append(ends, r.paid(r.above+1))
	}
	var out []string
	add := func(class string, cost, bill decimal.Decimal) {
		if cost.Cmp(bill) > 0 && !slices.Contains(out, class) {
			out = append(out, class)
		}
	}
	for _, c := range ends {
		add("input", c.in, price.in)
		add("output", c.out, price.out)
		add("cache read", c.readRate(), price.readRate())
		add("cache write", c.writeRate(), price.writeRate())
	}
	return out
}

// arm is the rung as a SuperAdmin reads it, held to price.
func (r rung) arm(price costs) Arm {
	a := Arm{Upstream: r.upstream, Provider: r.provider, Kind: kindOfRung(r.provider), Fee: feePercent(levies.multiple(r.provider)), List: r.cost.rate(), Over: r.over(price)}
	if r.long != nil {
		l := r.long.rate()
		a.Long, a.Above = &l, int(r.above)
	}
	return a
}

// kindOfRung is the path a family's rung runs on, by its catalog provider: a
// reseller's credit (a provider with a fee: OpenRouter's), else the provider's own
// API.
func kindOfRung(provider string) string {
	if levies.multiple(provider).Cmp(decimal.New(1, 0)) > 0 {
		return reseller
	}
	return kindOfRow(provider)
}

// misfit is why a model's route does not fit under the price it lists at: the first
// arm that costs us more than it bills, in which classes, or a route with no arm. ""
// when every arm fits. It names upstreams, so it is for the log and a SuperAdmin.
func misfit(m zenModel, rs []rung) string {
	if len(rs) == 0 {
		return "its route holds no arm"
	}
	price := costs{in: m.Base.In, out: m.Base.Out, read: m.Base.CacheRead, write: m.Base.CacheWrite}
	for _, r := range rs {
		if classes := r.over(price); len(classes) > 0 {
			return fmt.Sprintf("%s through %s costs more than %s bills in %s", r.upstream, r.provider, m.ID, strings.Join(classes, ", "))
		}
	}
	return ""
}

// guard refuses a model of a family that sets its own prices unless every arm of its
// route costs us no more than the model bills, in every token class, at a long
// prompt's price where an arm charges one and with the fee of a reseller whose credit
// buys it. What cannot be checked is not sent: a model with no price, or whose route
// was never read or holds no arm, is refused too. The refusal names no upstream; the
// log does. nil for a family priced any other way.
func (f *modelFamily) guard(ctx context.Context, model string) error {
	if f.prices == nil {
		return nil
	}
	m, ok := f.lookup(model)
	if !ok {
		return &apiError{status: http.StatusServiceUnavailable, msg: model + " has no price and is not served"}
	}
	rs, err := f.route(ctx)
	if err != nil {
		return &apiError{status: http.StatusServiceUnavailable, msg: model + " is not served right now: its route cannot be checked"}
	}
	if why := misfit(m, rs[strings.ToLower(m.ID)]); why != "" {
		return &apiError{status: http.StatusServiceUnavailable, msg: model + " is not served right now: its route costs more than it bills"}
	}
	return nil
}

const (
	// routeEvery is how long a read of a family's route stands before the next is
	// begun.
	routeEvery = time.Minute
	// routeRetry is how long a first read that failed answers its failure before it
	// is tried again, so an unreachable catalog is not asked on every request.
	routeRetry = 15 * time.Second
	// routeWait bounds one read.
	routeWait = 20 * time.Second
)

// ladder is a family's route as its admin catalog was last read: each model's
// rungs.
type ladder struct {
	mu    sync.Mutex
	rungs map[string][]rung // by lowercase id; nil until a read holds
	at    time.Time         // when rungs were read
	tried time.Time         // when the last read failed
	err   error             // why it failed
	busy  bool              // a read runs beside the requests
}

// route is the family's route as last read. The first read is made here and waited
// for, whether or not the request that asked is still there; a read older than
// routeEvery is made again beside the request, which reads the last one meanwhile. A
// failed read keeps the last one; a first read that failed answers its failure for
// routeRetry.
func (f *modelFamily) route(ctx context.Context) (map[string][]rung, error) {
	l := &f.ladder
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rungs == nil {
		if l.err != nil && time.Since(l.tried) < routeRetry {
			return nil, l.err
		}
		rs, err := readRoute(context.WithoutCancel(ctx), f)
		l.keep(f, rs, err)
		if err != nil {
			return nil, err
		}
		return l.rungs, nil
	}
	if time.Since(l.at) >= routeEvery && !l.busy {
		l.busy = true
		go func() {
			rs, err := readRoute(context.Background(), f)
			l.mu.Lock()
			defer l.mu.Unlock()
			l.busy = false
			l.keep(f, rs, err)
		}()
	}
	return l.rungs, nil
}

// keep records one read with l.mu held: a route that was read replaces the last, and
// every model it prices above what the model bills is said; a read that failed keeps
// the last.
func (l *ladder) keep(f *modelFamily, rs map[string][]rung, err error) {
	if err != nil {
		l.err, l.tried = err, time.Now()
		log.Warn("%s: its route: %v", f.name, err)
		return
	}
	l.rungs, l.at, l.err = rs, time.Now(), nil
	for _, id := range slices.Sorted(maps.Keys(rs)) {
		if m, ok := f.lookup(id); ok {
			if why := misfit(m, rs[id]); why != "" {
				log.Error("guard: %s is refused: %s", id, why)
			}
		}
	}
}

// stale marks the route read due, so the next request reads it again.
func (l *ladder) stale() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.at = time.Time{}
}

// last is the route as last read, without reading it; nil before the first.
func (l *ladder) last() map[string][]rung {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rungs
}

// adminPrice is one price as the family's admin catalog writes it: USD a million
// tokens, decimal strings, "" for a class it does not state.
type adminPrice struct {
	In         string `json:"in"`
	Out        string `json:"out"`
	CacheRead  string `json:"cache_read"`
	CacheWrite string `json:"cache_write"`
}

func (p adminPrice) costs() costs {
	d := func(s string) decimal.Decimal {
		v, err := decimal.Parse(strings.TrimSpace(s))
		if err != nil || v.Sign() < 0 {
			return decimal.New(0, 0)
		}
		return v
	}
	return costs{in: d(p.In), out: d(p.Out), read: d(p.CacheRead), write: d(p.CacheWrite)}
}

// adminCatalog is the part of the family's admin catalog (GET /v1/admin/catalog,
// hanzo-inc/engine admin.rs) the guard reads: each model's route.
type adminCatalog struct {
	Models map[string]struct {
		Disabled bool `json:"disabled"`
		Route    []struct {
			Provider string     `json:"provider"`
			Upstream string     `json:"upstream"`
			Cost     adminPrice `json:"cost"`
			Long     *struct {
				Above int64 `json:"above"`
				adminPrice
			} `json:"long"`
		} `json:"route"`
	} `json:"models"`
}

// rungsOf reads an admin catalog body into each model's rungs, by lowercase id.
func rungsOf(body []byte) (map[string][]rung, error) {
	var c adminCatalog
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, err
	}
	out := make(map[string][]rung, len(c.Models))
	for id, m := range c.Models {
		if m.Disabled {
			continue
		}
		var rs []rung
		for _, t := range m.Route {
			r := rung{provider: t.Provider, upstream: t.Upstream, cost: t.Cost.costs()}
			if t.Long != nil && t.Long.Above > 0 {
				l := t.Long.costs()
				r.long, r.above = &l, t.Long.Above
			}
			rs = append(rs, r)
		}
		out[strings.ToLower(id)] = rs
	}
	return out, nil
}

// readRoute reads the family's admin catalog (the engine's GET /v1/admin/catalog,
// under the admin token ai holds for its families) into each model's rungs.
func readRoute(ctx context.Context, f *modelFamily) (map[string][]rung, error) {
	base := f.baseURL()
	if base == "" {
		return nil, fmt.Errorf("%s is not configured", f.name)
	}
	token := strings.TrimSpace(object.ResolveKey("ZEN_ADMIN_TOKEN"))
	if token == "" {
		return nil, fmt.Errorf("ZEN_ADMIN_TOKEN is not set")
	}
	ctx, cancel := context.WithTimeout(ctx, routeWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/admin/catalog", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := zenDiscoveryClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/admin/catalog: %d %s", resp.StatusCode, upstreamErrorMessage(body))
	}
	return rungsOf(body)
}

// day is the window the ledger reports.
const day = 24 * time.Hour

// reckon is what the family's calls did over the last day, read from the usage
// ledger.
func reckon(ctx context.Context, f *modelFamily, now time.Time) (Ledger, error) {
	rows, err := object.RoutedRows(ctx, f.name, now.Add(-day))
	if err != nil {
		return Ledger{}, err
	}
	return books(rows, now), nil
}

// books folds the day's rows: the answers, the arms that failed first, those a
// reseller's credit bought, what they cost and billed and the margin, and every
// loss, in all and by model and arm.
func books(rows []object.Routed, now time.Time) Ledger {
	l := Ledger{Since: now.Add(-day), Until: now}
	routes := map[string]*Route{}
	for _, r := range rows {
		if r.Status != "success" && r.Status != "failover" {
			continue
		}
		k := r.Model + "\x00" + r.Served + "\x00" + r.Vendor
		rt := routes[k]
		if rt == nil {
			rt = &Route{Model: r.Model, Served: r.Served, Vendor: r.Vendor}
			routes[k] = rt
		}
		n := int(r.Requests)
		if r.Status == "failover" {
			l.Failovers += n
			rt.Failovers += n
			continue
		}
		l.Requests += n
		if r.Vendor != "" && levies.multiple(r.Vendor).Cmp(decimal.New(1, 0)) > 0 {
			l.Fallbacks += n
		}
		margin := r.KnownNano - r.CostNano
		l.Uncosted += int(r.Uncosted)
		l.Losses += int(r.Losses)
		l.LossNano += r.LossNano
		l.CostNano += r.CostNano
		l.BilledNano += r.BilledNano
		l.MarginNano += margin
		rt.Requests += n
		rt.Uncosted += int(r.Uncosted)
		rt.CostNano += r.CostNano
		rt.BilledNano += r.BilledNano
		rt.MarginNano += margin
		rt.LossNano += r.LossNano
	}
	if l.Requests > 0 {
		l.FallbackShare = float64(l.Fallbacks) / float64(l.Requests)
	}
	l.Routes = make([]Route, 0, len(routes))
	for _, rt := range routes {
		l.Routes = append(l.Routes, *rt)
	}
	slices.SortFunc(l.Routes, func(a, b Route) int {
		return strings.Compare(a.Model+"\x00"+a.Served+"\x00"+a.Vendor, b.Model+"\x00"+b.Served+"\x00"+b.Vendor)
	})
	return l
}

// armUse is one arm of a call as its row records it (usageRecord.Arms).
type armUse struct {
	Upstream string `json:"upstream"`
	Provider string `json:"provider"`
	Answered bool   `json:"answered,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Rate is the provider's price at the time, a million tokens; Fee the
	// reseller's on top of it, percent.
	Rate       *Rate  `json:"rate,omitempty"`
	Fee        string `json:"fee,omitempty"`
	Fresh      int    `json:"fresh,omitempty"`
	Cached     int    `json:"cached,omitempty"`
	Written    int    `json:"written,omitempty"`
	Completion int    `json:"completion,omitempty"`
	Reasoning  int    `json:"reasoning,omitempty"`
}

// trail is the arms a call of model ran, as JSON: the one that answered, with its
// rates at the time and the tokens it was paid for, then each that failed first.
func trail(f *modelFamily, model string, sv serving, t tokens) string {
	var out []armUse
	if sv.arm != "" {
		a := armUse{Upstream: sv.arm, Provider: sv.vendor, Answered: true, Fresh: t.fresh, Cached: t.cached, Written: t.written, Completion: t.completion, Reasoning: t.reasoning}
		if r, ok := f.rungOf(model, sv); ok {
			c := r.cost
			if r.long != nil && int64(t.prompt()) > r.above {
				c = *r.long
			}
			rate := c.rate()
			a.Rate, a.Fee = &rate, feePercent(levies.multiple(r.provider))
		}
		out = append(out, a)
	}
	for _, a := range arms(sv.failover) {
		out = append(out, armUse{Upstream: a.upstream, Provider: a.provider, Reason: a.reason})
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// rungOf is the rung of model's route as last read that an answer ran on.
func (f *modelFamily) rungOf(model string, sv serving) (rung, bool) {
	m, ok := f.lookup(model)
	if !ok {
		return rung{}, false
	}
	for _, r := range f.ladder.last()[strings.ToLower(m.ID)] {
		if r.upstream == sv.arm && r.provider == sv.vendor {
			return r, true
		}
	}
	return rung{}, false
}

// answered checks the guard's work after the fact: a priced model that answered on an
// arm its route as read does not hold, or on one that costs more than the model
// bills, is said, and its route is read again on the next request. The guard held the
// route it last read, and the answer is the newer word.
func (f *modelFamily) answered(model string, sv serving) {
	m, ok := f.lookup(model)
	if !ok || !m.priced() || sv.arm == "" {
		return
	}
	r, held := f.rungOf(model, sv)
	switch {
	case !held:
		log.Error("guard: %s answered on %s (%s), which its route as read does not hold", model, sv.arm, sv.vendor)
	case len(r.over(costs{in: m.Base.In, out: m.Base.Out, read: m.Base.CacheRead, write: m.Base.CacheWrite})) > 0:
		log.Error("guard: %s answered on %s (%s), which costs more than it bills", model, sv.arm, sv.vendor)
	default:
		return
	}
	f.ladder.stale()
}

// depthOf is how hard a request asks its model to think, as the family reads it
// (hanzo-inc/engine thinking.rs): reasoning_effort (or a Responses reasoning.effort)
// low, medium, high, or max (xhigh reads max), anything else none; an Anthropic
// thinking budget by its size, disabled none; "default" when it states no depth and
// gets its model's own. The row records it beside the arm that answered.
func depthOf(body []byte) string {
	var r struct {
		Effort    *string `json:"reasoning_effort"`
		Reasoning *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Thinking *struct {
			Type   string `json:"type"`
			Budget int64  `json:"budget_tokens"`
		} `json:"thinking"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "default"
	}
	switch {
	case r.Effort != nil:
		return effortDepth(*r.Effort)
	case r.Reasoning != nil && r.Reasoning.Effort != "":
		return effortDepth(r.Reasoning.Effort)
	case r.Thinking != nil && r.Thinking.Type == "disabled":
		return "none"
	case r.Thinking != nil && r.Thinking.Budget > 0:
		return budgetDepth(r.Thinking.Budget)
	}
	return "default"
}

// effortDepth is an effort as a depth: low, medium, high, max; xhigh is max, and
// none, minimal or anything else none.
func effortDepth(e string) string {
	switch strings.ToLower(strings.TrimSpace(e)) {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "max":
		return "max"
	}
	return "none"
}

// budgetDepth is an Anthropic thinking budget as a depth: none at zero, low under
// 4,096 tokens, medium under 16,384, max from there.
func budgetDepth(tokens int64) string {
	switch {
	case tokens <= 0:
		return "none"
	case tokens < 4096:
		return "low"
	case tokens < 16384:
		return "medium"
	}
	return "max"
}
