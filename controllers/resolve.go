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

// resolve.go — how the gateway reaches a model, in order, stated.
//
// A model is reached first on a key of the provider that sells it directly (its
// maker's API, or a vendor's own on a route row), then through DigitalOcean's
// inference, then through OpenRouter, the fallback when no key of ours or the org's
// serves it. A call through OpenRouter costs its price and OpenRouter's fee, so the
// order is also the order of cost: each later path costs at least the earlier.
//
// The order is built in where the gateway builds it: a maker's key is asked before
// the family that resells its model (native.go), and OpenRouter's copy of a route's
// model is its last candidate (openrouterTail). Between a route row's own providers
// the row's declared order is what candidates asks (an operator's intent), so every
// row of conf/models.yaml lists a direct provider before DigitalOcean, and a test
// holds it. Paths states the order for one model; Blend costs a model by its first
// keyed path.

import (
	"strings"

	"github.com/hanzoai/ai/object"
)

// Kinds of path, in the order a model is asked of them.
const (
	// direct is the provider's own API, on a key Hanzo or the org holds.
	direct = "direct"
	// family is a Hanzo family service (Zen, Enso), which walks its own arms.
	family = "family"
	// digitalocean is DigitalOcean's inference, reselling a provider's model.
	digitalocean = "do"
	// reseller is OpenRouter, the fallback.
	reseller = "openrouter"
)

// Path is one way the gateway reaches a model.
type Path struct {
	// Provider is who answers on it: a maker ("anthropic"), a provider row
	// ("openai", "do-ai"), a family ("enso") or "openrouter".
	Provider string `json:"provider"`
	// Kind is direct, family, do or openrouter.
	Kind string `json:"kind"`
	// Keyed says a key for it is configured: a maker's key that lists the model, a
	// provider row with a credential, a family or a reseller with an address.
	Keyed bool `json:"keyed"`
}

// Paths is every way the gateway reaches model id for org, in the order a call
// asks them before any vendor has refused it: a maker's key, the route row's
// providers as declared, OpenRouter's copy of the route's model, the family's
// address. A model no path reaches has none.
func Paths(id, org string) []Path {
	var out []Path
	add := func(p Path) {
		for _, q := range out {
			if q.Provider == p.Provider {
				return
			}
		}
		out = append(out, p)
	}
	fam, served := familyServing(id)
	if zm, ok := familyLookup(id); ok && served && fam.native != nil {
		if _, makes := fam.native.spelled(zm.ID); makes {
			add(Path{Provider: fam.native.name, Kind: direct, Keyed: fam.native.serves(zm.ID)})
		}
	}
	if r := resolveModelRouteForOrg(id, org); r != nil {
		names := []string{r.providerName}
		for _, f := range r.fallbacks {
			names = append(names, f.providerName)
		}
		for _, name := range names {
			if name == "" {
				continue
			}
			if f := familyNamed(name); f != nil {
				add(Path{Provider: f.name, Kind: kindOf(f), Keyed: f.enabled()})
				continue
			}
			add(Path{Provider: name, Kind: kindOfRow(name), Keyed: keyed(org, name)})
		}
		for _, c := range openrouterTail(r, false) {
			add(Path{Provider: c.provider, Kind: reseller, Keyed: true})
		}
	}
	if served && fam != nil {
		add(Path{Provider: fam.name, Kind: kindOf(fam), Keyed: fam.enabled()})
	}
	return out
}

// kindOf is the kind of path a family's address is.
func kindOf(f *modelFamily) string {
	if f == openrouterFam {
		return reseller
	}
	return family
}

// kindOfRow is the kind of path a provider row is: DigitalOcean's inference, or a
// provider's own API.
func kindOfRow(name string) string {
	if strings.EqualFold(name, "do-ai") || strings.HasPrefix(strings.ToLower(name), "digitalocean") {
		return digitalocean
	}
	return direct
}

// keyed reports that a provider row named name, the org's own or the platform's,
// carries a credential.
func keyed(org, name string) bool {
	p, err := object.GetModelProviderByNameForOrg(org, name)
	return err == nil && p != nil && strings.TrimSpace(p.ClientSecret) != ""
}
