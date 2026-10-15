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

// rates.go — what an Enso model costs, as it is routed.
//
// An Enso SKU is a route over arms — the family's catalog names each upstream, the
// provider whose account runs it, and what a million tokens of it cost — and a call
// bills what its arms cost us plus the platform's fee (fee.go). So an Enso SKU has
// no one price. It has three, each the platform's fee over a cost:
//
//	from     its cheapest arm, class by class: the least a token of it bills;
//	typical  what its calls cost over the last day, measured from the ledger (or
//	         its last `thin` calls when the day held fewer): each call's cost of
//	         record split between its token classes by its arm's rates, summed, and
//	         divided by the tokens, class by class;
//	up to    its dearest arm, a long prompt's price included: the most.
//
// An arm costs us its provider's price and, where a reseller's credit bought it, the
// reseller's fee (OpenRouter's 5.5%). from and up to follow the catalog, read every
// rateEvery; typical is measured at start, once a day and when asked (Reprice: POST
// /v1/pricing/sync). /v1/models, /v1/pricing and the quote all read the same
// computation, so one model cannot carry two prices by where it is read.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

const (
	// rateDay is the window typical is measured over.
	rateDay = 24 * time.Hour
	// rateBack is how far back a thin window reaches for its last calls.
	rateBack = 30 * 24 * time.Hour
	// thin is the fewest calls a typical rate is measured over: a model the day
	// served fewer is measured over its last thin calls instead.
	thin = 50
	// rateRows bounds one day's read of the ledger.
	rateRows = 200_000
	// rateEvery is how often the catalog's arms are read again, so from and up to
	// follow a catalog edit within it.
	rateEvery = 5 * time.Minute
	// measureEvery is how often typical is measured.
	measureEvery = 24 * time.Hour
)

// Rate is a price in US dollars a million tokens.
type Rate struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// Window is the calls a typical rate was measured over.
type Window struct {
	Since    time.Time `json:"since"`
	Until    time.Time `json:"until"`
	Requests int       `json:"requests"`
	// Basis is "24h" (every call of the last day), "last" (the last calls, the
	// day having held fewer than thin), or "" when no call was measured.
	Basis string `json:"basis"`
}

// Arm is one route of a model: its upstream, the provider whose account runs it,
// and the provider's price.
type Arm struct {
	Upstream string `json:"upstream"`
	Provider string `json:"provider"`
	// Fee is the reseller's fee on the provider's price, percent; "0" for none.
	Fee  string `json:"fee"`
	List Rate   `json:"list"`
	// Long is the provider's price for a prompt past Above tokens.
	Long  *Rate `json:"long,omitempty"`
	Above int   `json:"above,omitempty"`
}

// Quote is one model's price at one reasoning depth, the platform's fee applied.
type Quote struct {
	Model string `json:"model"`
	// Reasoning is the depth quoted: none, low, medium, high, max, default; ""
	// for every depth together.
	Reasoning string `json:"reasoning"`
	From      Rate   `json:"from"`
	// Typical is nil when no call was measured.
	Typical *Rate  `json:"typical"`
	UpTo    Rate   `json:"up_to"`
	Fee     string `json:"fee"`
	BYOK    bool   `json:"byok,omitempty"`
	Window  Window `json:"window"`
	// Arms names the upstreams: a SuperAdmin's read alone carries them.
	Arms []Arm `json:"arms,omitempty"`
}

// Route is one model's answers through one provider's account.
type Route struct {
	Model      string `json:"model"`
	Vendor     string `json:"vendor"`
	Requests   int    `json:"requests"`
	CostNano   int64  `json:"cost_nano"`
	BilledNano int64  `json:"billed_nano"`
	FeeNano    int64  `json:"fee_nano"`
	LossNano   int64  `json:"loss_nano"`
}

// Ledger is what the family's calls did over the last day.
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
	// Losses are answers that cost us more than they billed: none while every call
	// bills its cost plus the fee. Each is a row (loss_nano).
	Losses     int     `json:"losses"`
	LossNano   int64   `json:"loss_nano"`
	CostNano   int64   `json:"cost_nano"`
	BilledNano int64   `json:"billed_nano"`
	FeeNano    int64   `json:"fee_nano"`
	Routes     []Route `json:"routes"`
}

// Board is one computation: every model's quote at every depth measured, and the
// ledger it was measured from.
type Board struct {
	Computed time.Time `json:"computed"`
	Measured time.Time `json:"measured"`
	Quotes   []Quote   `json:"quotes"`
	Ledger   Ledger    `json:"ledger"`
	// Error is why the last measure failed, "" when it held.
	Error string `json:"error,omitempty"`
}

// costs is a price per million tokens before the platform's fee.
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

func (c costs) zero() bool { return c.in.IsZero() && c.out.IsZero() }

func (c costs) rate() Rate {
	f := func(d decimal.Decimal) float64 { v, _ := strconv.ParseFloat(d.String(), 64); return v }
	return Rate{Input: f(c.in), Output: f(c.out), CacheRead: f(c.read), CacheWrite: f(c.write)}
}

// least and most are the cheaper and dearer of two costs, class by class.
func least(a, b costs) costs {
	return costs{minDec(a.in, b.in), minDec(a.out, b.out), minDec(a.read, b.read), minDec(a.write, b.write)}
}
func most(a, b costs) costs {
	return costs{maxDecimal(a.in, b.in), maxDecimal(a.out, b.out), maxDecimal(a.read, b.read), maxDecimal(a.write, b.write)}
}

func minDec(a, b decimal.Decimal) decimal.Decimal {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
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

// arm is the rung as a SuperAdmin reads it.
func (r rung) arm() Arm {
	a := Arm{Upstream: r.upstream, Provider: r.provider, Fee: feePercent(levies.multiple(r.provider)), List: r.cost.rate()}
	if r.long != nil {
		l := r.long.rate()
		a.Long, a.Above = &l, int(r.above)
	}
	return a
}

// cell is one model at one depth, before the platform's fee.
type cell struct {
	model, depth string
	from, upTo   costs
	typical      *costs
	window       Window
	rungs        []rung
}

// quote is p with the platform's fee applied.
func (p cell) quote(byok, admin bool) Quote {
	m := platform(byok)
	q := Quote{Model: p.model, Reasoning: p.depth, From: p.from.times(m).rate(), UpTo: p.upTo.times(m).rate(), Fee: feePercent(m), BYOK: byok, Window: p.window}
	if p.typical != nil {
		t := p.typical.times(m).rate()
		q.Typical = &t
	}
	if admin {
		for _, r := range p.rungs {
			q.Arms = append(q.Arms, r.arm())
		}
	}
	return q
}

// board is the last computation; nil until the first.
type board struct {
	computed, measured time.Time
	quotes             map[string]cell // by quoteKey
	ledger             Ledger
	err                string
	// rows is the measure the quotes were computed from, kept so a catalog read can
	// recompute from and up to without reading the ledger again.
	rows []object.PricedRow
	last []object.PricedRow
}

var (
	current atomic.Pointer[board]
	// ladders is each cost-plus SKU's rungs, from the family's catalog.
	ladders atomic.Pointer[map[string][]rung]
	// repricing serializes computations.
	repricing sync.Mutex
)

func quoteKey(model, depth string) string { return strings.ToLower(model) + "\x00" + depth }

// Prices is every quote of the last computation, the platform's fee (BYOK's when
// byok) applied. A SuperAdmin's read (admin) carries each quote's arms.
func Prices(byok, admin bool) Board {
	b := current.Load()
	if b == nil {
		return Board{}
	}
	out := Board{Computed: b.computed, Measured: b.measured, Ledger: b.ledger, Error: b.err}
	keys := make([]string, 0, len(b.quotes))
	for k := range b.quotes {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out.Quotes = append(out.Quotes, b.quotes[k].quote(byok, admin))
	}
	return out
}

// Price quotes model at depth (none, low, medium, high, max, default; "" for every
// depth together). A depth no call was measured at quotes the model's from and up
// to with nothing typical. ok is false for a model not priced at cost.
func Price(model, depth string, byok, admin bool) (Quote, bool) {
	b := current.Load()
	if b == nil {
		return Quote{}, false
	}
	depth = normalDepth(depth)
	if p, ok := b.quotes[quoteKey(model, depth)]; ok {
		return p.quote(byok, admin), true
	}
	p, ok := b.quotes[quoteKey(model, "")]
	if !ok {
		return Quote{}, false
	}
	p.depth, p.typical, p.window = depth, nil, Window{}
	return p.quote(byok, admin), true
}

// normalDepth spells a depth as the ledger does: every depth an effort names reads
// as depthOf reads it, and "" stays "" (every depth).
func normalDepth(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	switch d {
	case "", "default":
		return d
	}
	return effortDepth(d)
}

// Blend quotes a set of models a caller composed (the benchmark's presets): from
// its cheapest model, up to its dearest, nothing typical, since no call routed it.
// A model priced at cost reads its own from and up to; any other its price list,
// a resold model's at its vendor's price and the reseller's fee, the same cost of
// record its calls carry.
func Blend(ids []string, byok bool) (Quote, error) {
	if len(ids) == 0 {
		return Quote{}, fmt.Errorf("a blend names at least one model")
	}
	var from, upTo costs
	var listed []Arm
	for i, id := range ids {
		id = strings.TrimSpace(id)
		lo, hi, a, err := costOfModel(id)
		if err != nil {
			return Quote{}, err
		}
		if i == 0 {
			from, upTo = lo, hi
		} else {
			from, upTo = least(from, lo), most(upTo, hi)
		}
		listed = append(listed, a)
	}
	m := platform(byok)
	return Quote{Model: strings.Join(ids, ","), From: from.times(m).rate(), UpTo: upTo.times(m).rate(), Fee: feePercent(m), BYOK: byok, Arms: listed}, nil
}

// costOfModel is the least and the most a token of id costs us, and the arm it
// is quoted as.
func costOfModel(id string) (lo, hi costs, a Arm, err error) {
	if p, ok := Price(id, "", false, false); ok {
		b := current.Load()
		q := b.quotes[quoteKey(id, "")]
		return q.from, q.upTo, Arm{Upstream: p.Model, Provider: ensoFam.name, Fee: "0", List: q.from.rate()}, nil
	}
	if zm, ok := familyLookup(id); ok && zm.priced() {
		if f, _ := familyServing(id); f == openrouterFam && zm.CostIn.Sign() > 0 {
			c := costs{in: zm.CostIn, out: zm.CostOut}
			m := levies.multiple(openrouterFam.name)
			return c.times(m), c.times(m), Arm{Upstream: zm.ID, Provider: openrouterFam.name, Fee: feePercent(m), List: c.rate()}, nil
		}
		c := costs{in: zm.Base.In, out: zm.Base.Out}
		return c, c, Arm{Upstream: zm.ID, Provider: zm.OwnedBy, Fee: "0", List: c.rate()}, nil
	}
	if p, ok := getModelPriceForOrgOK(id, ""); ok && !p.Variable && (p.InputPerMillion > 0 || p.OutputPerMillion > 0) {
		c := costs{in: decimal.MustParse(strconv.FormatFloat(p.InputPerMillion, 'f', -1, 64)), out: decimal.MustParse(strconv.FormatFloat(p.OutputPerMillion, 'f', -1, 64))}
		return c, c, Arm{Upstream: id, Fee: "0", List: c.rate()}, nil
	}
	return costs{}, costs{}, Arm{}, fmt.Errorf("model %q has no price", id)
}

// StartRates computes the board at start and keeps it: the catalog's arms every
// rateEvery, typical every measureEvery. A computation that fails is tried again at
// the next tick, and the last board stands meanwhile.
func StartRates() {
	ratesOnce.Do(func() {
		go func() {
			ctx := context.Background()
			if _, err := Reprice(ctx); err != nil {
				log.Warn("rates: %v", err)
			}
			for {
				time.Sleep(rateEvery)
				b := current.Load()
				var err error
				if b == nil || b.err != "" || time.Since(b.measured) >= measureEvery {
					_, err = Reprice(ctx)
				} else {
					err = rearm(ctx)
				}
				if err != nil {
					log.Warn("rates: %v", err)
				}
			}
		}()
	})
}

var ratesOnce sync.Once

// Reprice reads the family's arms and the last day's ledger and computes the board
// now. A ledger it cannot read keeps the last measure and says why on the board.
func Reprice(ctx context.Context) (Board, error) {
	repricing.Lock()
	defer repricing.Unlock()
	if err := readArms(ctx); err != nil {
		log.Warn("rates: the enso catalog's arms: %v", err)
	}
	now := time.Now().UTC()
	day, derr := object.PricedRows(ctx, ensoFam.name, now.Add(-rateDay), rateRows)
	var last []object.PricedRow
	if derr == nil {
		last, derr = object.LastPricedRows(ctx, ensoFam.name, now.Add(-rateBack), thin)
	}
	b := &board{computed: now, measured: now}
	if derr != nil {
		b.err = derr.Error()
		if prev := current.Load(); prev != nil {
			b.rows, b.last, b.measured = prev.rows, prev.last, prev.measured
		}
	} else {
		b.rows, b.last = day, last
	}
	compute(b, now)
	current.Store(b)
	ratesAt.Add(1)
	if derr != nil {
		return Prices(false, true), fmt.Errorf("the ledger: %w", derr)
	}
	return Prices(false, true), nil
}

// rearm reads the catalog's arms again and recomputes from and up to over the
// last measure.
func rearm(ctx context.Context) error {
	repricing.Lock()
	defer repricing.Unlock()
	err := readArms(ctx)
	prev := current.Load()
	b := &board{computed: time.Now().UTC()}
	if prev != nil {
		b.measured, b.rows, b.last, b.err = prev.measured, prev.rows, prev.last, prev.err
	}
	compute(b, b.computed)
	current.Store(b)
	ratesAt.Add(1)
	return err
}

// ratesAt moves whenever a board is stored, so the model listing builds again.
var ratesAt atomic.Int64

// compute fills b's quotes and ledger from its rows and the arms last read.
func compute(b *board, now time.Time) {
	rungs := map[string][]rung{}
	if p := ladders.Load(); p != nil {
		rungs = *p
	}
	b.quotes = map[string]cell{}
	for _, z := range ensoFam.snapshot() {
		if !z.priced() {
			continue
		}
		model := strings.ToLower(z.ID)
		rs := rungs[model]
		from, upTo, ok := span(rs)
		if !ok {
			// No arm read: the listing's own price, the most a token bills, bounds
			// both, so neither reads below what a call can cost.
			c := costs{in: z.Base.In, out: z.Base.Out, read: z.Base.CacheRead, write: z.Base.CacheWrite}.times(inverse(platform(false)))
			from, upTo = c, c
		}
		groups := map[string][]object.PricedRow{}
		basis := map[string]string{}
		pick(model, b.rows, b.last, now, groups, basis)
		for depth, rows := range groups {
			p := cell{model: z.ID, depth: depth, from: from, upTo: upTo, rungs: rs}
			p.typical, p.window = typical(rows, rs, upTo)
			p.window.Basis = basis[depth]
			if p.window.Requests == 0 {
				p.window.Basis = ""
			}
			b.quotes[quoteKey(model, depth)] = p
		}
		if _, ok := b.quotes[quoteKey(model, "")]; !ok {
			b.quotes[quoteKey(model, "")] = cell{model: z.ID, from: from, upTo: upTo, rungs: rs}
		}
	}
	b.ledger = books(b.rows, now)
}

// inverse is 1/m, to eighteen places.
func inverse(m decimal.Decimal) decimal.Decimal {
	if m.Sign() <= 0 {
		return decimal.New(1, 0)
	}
	return decimal.New(1, 0).Quo(m, 18)
}

// span is the cheapest and the dearest a token of the rungs costs us, class by
// class, a long prompt's price among the dearest. A rung of no cost (a free lane) is
// no arm of a priced model.
func span(rs []rung) (from, upTo costs, ok bool) {
	for _, r := range rs {
		if r.cost.zero() {
			continue
		}
		lo := r.paid(0)
		hi := lo
		if r.long != nil {
			hi = most(hi, r.paid(r.above+1))
		}
		if !ok {
			from, upTo, ok = lo, hi, true
			continue
		}
		from, upTo = least(from, lo), most(upTo, hi)
	}
	return from, upTo, ok
}

// pick groups a model's answered, costed calls by depth, and all of them under "":
// the last day's when it holds thin or more, else the last thin of each.
func pick(model string, day, last []object.PricedRow, now time.Time, groups map[string][]object.PricedRow, basis map[string]string) {
	measured := func(r object.PricedRow) bool {
		return strings.EqualFold(r.Model, model) && r.Status == "success" && !r.Uncosted && r.CostNano > 0
	}
	byDay := map[string][]object.PricedRow{}
	for _, r := range day {
		if measured(r) {
			byDay[r.Reasoning] = append(byDay[r.Reasoning], r)
		}
	}
	byLast := map[string][]object.PricedRow{}
	for _, r := range last {
		if measured(r) {
			byLast[r.Reasoning] = append(byLast[r.Reasoning], r)
		}
	}
	depths := map[string]bool{}
	for d := range byDay {
		depths[d] = true
	}
	for d := range byLast {
		depths[d] = true
	}
	var all []object.PricedRow
	allBasis := "24h"
	for d := range depths {
		rows, why := byDay[d], "24h"
		if len(rows) < thin {
			rows, why = byLast[d], "last"
			allBasis = "last"
		}
		if len(rows) == 0 {
			continue
		}
		groups[d], basis[d] = rows, why
		all = append(all, rows...)
	}
	if len(all) > 0 {
		groups[""], basis[""] = all, allBasis
	}
}

// typical is what a token of rows cost us on average, class by class: each row's
// cost of record split between fresh input, cached input, input written to the
// cache and output by its arm's rates, and each class summed over its tokens. Each
// class so lies between its cheapest arm and its dearest. A class no token of was
// measured reads at its dearest; a cache class none was, at nothing.
func typical(rows []object.PricedRow, rs []rung, upTo costs) (*costs, Window) {
	var w Window
	var nano [4]decimal.Decimal
	var count [4]int64
	for i := range nano {
		nano[i] = decimal.New(0, 0)
	}
	for _, r := range rows {
		ru, ok := rungOf(rs, r.Served, r.Vendor)
		if !ok {
			continue
		}
		c := ru.paid(r.Prompt)
		n := [4]int64{max(r.Prompt-r.Cached-r.Written, 0), r.Cached, r.Written, r.Completion}
		part := [4]decimal.Decimal{
			c.in.Mul(decimal.New(n[0], 0)),
			c.readRate().Mul(decimal.New(n[1], 0)),
			c.writeRate().Mul(decimal.New(n[2], 0)),
			c.out.Mul(decimal.New(n[3], 0)),
		}
		sum := part[0].Add(part[1]).Add(part[2]).Add(part[3])
		if sum.Sign() <= 0 {
			continue
		}
		cost := decimal.New(costOfRow(r, rs), 0)
		for i := range part {
			nano[i] = nano[i].Add(cost.Mul(part[i]).Quo(sum, 9))
			count[i] += n[i]
		}
		if w.Requests == 0 || r.At.Before(w.Since) {
			w.Since = r.At
		}
		if r.At.After(w.Until) {
			w.Until = r.At
		}
		w.Requests++
	}
	if w.Requests == 0 {
		return nil, Window{}
	}
	// nano-dollars a token are dollars a million tokens over a thousand.
	per := func(i int) decimal.Decimal {
		return nano[i].Quo(decimal.New(count[i], 0), 18).Quo(decimal.New(1000, 0), 18)
	}
	t := costs{in: upTo.in, out: upTo.out, read: decimal.New(0, 0), write: decimal.New(0, 0)}
	if count[0] > 0 {
		t.in = per(0)
	}
	if count[1] > 0 {
		t.read = per(1)
	}
	if count[2] > 0 {
		t.write = per(2)
	}
	if count[3] > 0 {
		t.out = per(3)
	}
	return &t, w
}

// rungOf is the rung a call ran on: its upstream through its provider, else its
// upstream through any, else the model's first rung that costs anything.
func rungOf(rs []rung, served, vendor string) (rung, bool) {
	for _, r := range rs {
		if r.upstream == served && r.provider == vendor {
			return r, true
		}
	}
	for _, r := range rs {
		if r.upstream == served {
			return r, true
		}
	}
	for _, r := range rs {
		if !r.cost.zero() {
			return r, true
		}
	}
	return rung{}, false
}

// costOfRow is a row's cost of record, nano-dollars. A row written before the
// ledger named its vendor states its family's cost alone, and is costed here at the
// dearest reseller fee of the model's rungs, the fee the call most likely paid.
func costOfRow(r object.PricedRow, rs []rung) int64 {
	if r.Vendor != "" {
		return r.CostNano
	}
	m := decimal.New(1, 0)
	for _, ru := range rs {
		m = maxDecimal(m, levies.multiple(ru.provider))
	}
	return nanoUp(decimal.New(r.CostNano, 9).Mul(m))
}

// books is what the day's rows did: answers, the arms that failed first, those a
// reseller's credit bought, the fee collected and every loss, by model and vendor.
func books(rows []object.PricedRow, now time.Time) Ledger {
	l := Ledger{Since: now.Add(-rateDay), Until: now}
	routes := map[string]*Route{}
	for _, r := range rows {
		switch r.Status {
		case "failover":
			l.Failovers++
			continue
		case "success":
		default:
			continue
		}
		l.Requests++
		if r.Vendor != "" && levies.multiple(r.Vendor).Cmp(decimal.New(1, 0)) > 0 {
			l.Fallbacks++
		}
		fee := int64(0)
		if !r.Uncosted && r.BilledNano > r.CostNano {
			fee = r.BilledNano - r.CostNano
		}
		l.CostNano += r.CostNano
		l.BilledNano += r.BilledNano
		l.FeeNano += fee
		if r.LossNano > 0 {
			l.Losses++
			l.LossNano += r.LossNano
		}
		k := r.Model + "\x00" + r.Vendor
		rt := routes[k]
		if rt == nil {
			rt = &Route{Model: r.Model, Vendor: r.Vendor}
			routes[k] = rt
		}
		rt.Requests++
		rt.CostNano += r.CostNano
		rt.BilledNano += r.BilledNano
		rt.FeeNano += fee
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
		if c := strings.Compare(a.Model, b.Model); c != 0 {
			return c
		}
		return strings.Compare(a.Vendor, b.Vendor)
	})
	return l
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
// hanzo-inc/engine admin.rs) the rates read: each model's route.
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

// adminWait bounds one read of the family's admin catalog.
const adminWait = 20 * time.Second

// readArms reads the Enso family's admin catalog (the engine's GET
// /v1/admin/catalog, under the admin token ai holds for its families) and keeps
// its rungs. A read that fails keeps the rungs last read.
func readArms(ctx context.Context) error {
	base := strings.TrimRight(ensoFam.baseURL(), "/")
	if base == "" {
		return fmt.Errorf("enso is not configured")
	}
	token := strings.TrimSpace(object.ResolveKey("ZEN_ADMIN_TOKEN"))
	if token == "" {
		return fmt.Errorf("ZEN_ADMIN_TOKEN is not set")
	}
	ctx, cancel := context.WithTimeout(ctx, adminWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/admin/catalog", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := zenDiscoveryClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /v1/admin/catalog: %d %s", resp.StatusCode, upstreamErrorMessage(body))
	}
	rs, err := rungsOf(body)
	if err != nil {
		return err
	}
	ladders.Store(&rs)
	return nil
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
func trail(model string, sv serving, t tokens) string {
	var rs []rung
	if p := ladders.Load(); p != nil {
		rs = (*p)[strings.ToLower(model)]
	}
	var out []armUse
	if sv.arm != "" {
		a := armUse{Upstream: sv.arm, Provider: sv.vendor, Answered: true, Fresh: t.fresh, Cached: t.cached, Written: t.written, Completion: t.completion, Reasoning: t.reasoning}
		for _, r := range rs {
			if r.upstream == sv.arm && r.provider == sv.vendor {
				c := r.cost
				if r.long != nil && int64(t.prompt()) > r.above {
					c = *r.long
				}
				rate := c.rate()
				a.Rate, a.Fee = &rate, feePercent(levies.multiple(r.provider))
				break
			}
		}
		out = append(out, a)
	}
	for _, m := range arms(sv.failover) {
		out = append(out, armUse{Upstream: m.upstream, Provider: m.provider, Reason: m.reason})
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

// depthOf is how hard a request asks its model to think, as the family reads it
// (hanzo-inc/engine thinking.rs): reasoning_effort (or a Responses reasoning.effort)
// low, medium, high, or max (xhigh reads max), anything else none; an Anthropic
// thinking budget by its size, disabled none; "default" when it states no depth and
// gets its model's own.
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
