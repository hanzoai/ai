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

// fee.go — what a price adds to a cost.
//
// A call is priced at what it cost us, and the platform's fee on top: the fee is
// the margin, and there is no other. The platform states two (object.Fee):
// "hanzo", on a call Hanzo's own accounts paid for (5.5%, OpenRouter's own rate),
// and "hanzo_byok", on a call the org's own provider key paid for (1%). A vendor's
// fee is part of the cost instead: OpenRouter sells its credit at 5.5% over what a
// call spends of it, so a call bought through OpenRouter cost us its stated price
// plus that fee, and the platform's fee goes on top of both.
//
// Every fee is the host's operator setting (cloud: the usage policy's fees,
// apps/flags); ai keeps none. A host that states none bills at cost: never below
// it, and never at a margin nobody chose.

import (
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

const (
	// hanzoFee is the platform's fee on a call Hanzo's accounts paid for.
	hanzoFee = "hanzo"
	// byokFee is the platform's fee on a call the org's own provider key paid for.
	byokFee = "hanzo_byok"
)

// feeEvery is how long a fee read stands. A stale one still answers while the next
// read runs, so no call ever waits on the host for a fee it has read before.
const feeEvery = time.Minute

// feeRead is one read of a fee: its multiple (1 + percent / 100), and when.
type feeRead struct {
	multiple decimal.Decimal
	at       time.Time
}

// levy is every fee read, by name.
type levy struct {
	mu   sync.Mutex
	got  map[string]feeRead
	busy map[string]bool
}

var levies = &levy{got: map[string]feeRead{}, busy: map[string]bool{}}

var hundredth = decimal.New(1, 2)

// multiple is what a cost is multiplied by for the fee named: 1 + its percent. A
// fee the host has never stated is 1. A read the host answers with something that
// is not a percent keeps the last good read; the platform's own fees must also be
// more than zero, since a fee of zero is a call sold at cost.
func (b *levy) multiple(name string) decimal.Decimal {
	b.mu.Lock()
	r, ok := b.got[name]
	stale := ok && time.Since(r.at) >= feeEvery
	if stale && !b.busy[name] {
		b.busy[name] = true
		go b.read(name)
	}
	b.mu.Unlock()
	if ok {
		return r.multiple
	}
	b.read(name)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.got[name].multiple
}

// read asks the host for one fee and keeps what it says, if it is a fee.
func (b *levy) read(name string) {
	raw := object.Fee(name)
	m, err := feeMultiple(name, raw)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.busy[name] = false
	prev, had := b.got[name]
	switch {
	case err == nil:
		b.got[name] = feeRead{multiple: m, at: time.Now()}
	case had:
		log.Error("fee %s %q is not a fee, keeping %s: %v", name, raw, prev.multiple.String(), err)
		b.got[name] = feeRead{multiple: prev.multiple, at: time.Now()}
	default:
		log.Error("fee %s %q is not a fee and none was read before: billing at cost: %v", name, raw, err)
		b.got[name] = feeRead{multiple: decimal.New(1, 0), at: time.Now()}
	}
}

// errFee is a fee that is not one.
type errFee string

func (e errFee) Error() string { return string(e) }

// feeMultiple reads a percent as a multiple. "" is no fee. A platform fee is more
// than zero; a vendor's is zero or more.
func feeMultiple(name, raw string) (decimal.Decimal, error) {
	one := decimal.New(1, 0)
	if raw == "" {
		return one, nil
	}
	pct, err := decimal.Parse(raw)
	if err != nil {
		return decimal.Decimal{}, err
	}
	if pct.Sign() < 0 {
		return decimal.Decimal{}, errFee("a fee is not negative")
	}
	if (name == hanzoFee || name == byokFee) && pct.Sign() == 0 {
		return decimal.Decimal{}, errFee("the platform's fee is more than zero")
	}
	return one.Add(pct.Mul(hundredth)), nil
}

// forget drops every fee read, so the next asks the host again. Tests only.
func (b *levy) forget() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got = map[string]feeRead{}
	b.busy = map[string]bool{}
}

// platform is the multiple the platform's fee puts on a cost: byok for a call
// the org's own key paid for.
func platform(byok bool) decimal.Decimal {
	if byok {
		return levies.multiple(byokFee)
	}
	return levies.multiple(hanzoFee)
}

// feePercent is a multiple's percent as a decimal string ("5.5").
func feePercent(m decimal.Decimal) string {
	s := m.Sub(decimal.New(1, 0)).Mul(decimal.New(100, 0)).String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// costed is a stated cost, nano-dollars, with the fee of the costliest vendor
// whose credit bought any part of it on top: what the call cost us. A stated cost
// of zero is zero; nil states nothing.
func costed(stated *int64, vendors ...string) *int64 {
	if stated == nil {
		return nil
	}
	m := decimal.New(1, 0)
	for _, v := range vendors {
		if v == "" {
			continue
		}
		if f := levies.multiple(v); f.Cmp(m) > 0 {
			m = f
		}
	}
	n := nanoUp(decimal.New(*stated, 9).Mul(m))
	return &n
}

// plus is a cost with the platform's fee on top, nano-dollars, rounded up: the
// price of a call that cost nano.
func plus(nano int64, byok bool) int64 {
	if nano <= 0 {
		return 0
	}
	return nanoUp(decimal.New(nano, 9).Mul(platform(byok)))
}

// buyers are the vendors whose credit bought a family's answer: OpenRouter for an
// answer its own address gave; for a family's, the provider that ran the arm that
// answered and each that failed first (a failed arm may have cost something, and
// its share of the cost is in the family's statement); none for a maker's, whose own
// price it states.
func buyers(fam *modelFamily, sv serving) []string {
	if sv.maker != nil {
		return nil
	}
	if fam == openrouterFam {
		return []string{fam.name}
	}
	out := []string{sv.vendor}
	for _, a := range arms(sv.failover) {
		out = append(out, a.provider)
	}
	return out
}
