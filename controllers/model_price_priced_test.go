package controllers

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/ai/object"
)

var pricedSeq int64

// routeDB stands a route table up in memory and returns the org the routes are
// written under. Routes are global ("built-in"), which is what a deployment-wide
// default is.
func routeDB(t *testing.T) {
	t.Helper()
	n := atomic.AddInt64(&pricedSeq, 1)
	dsn := fmt.Sprintf("file:priced_%d?mode=memory&cache=shared", n)
	restore, err := object.UseMemoryDB(dsn, &object.ModelRoute{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)
}

func addRoute(t *testing.T, r *object.ModelRoute) {
	t.Helper()
	if _, err := object.AddModelRoute(r); err != nil {
		t.Fatalf("seed route %q: %v", r.ModelName, err)
	}
}

// TestPricedRouteCanCostNothing is the whole point of the column: a route may say
// the call costs the caller nothing, and the balance gate must believe it.
//
// costsNothing is the predicate the completion path consults before reading any
// balance, so a model answered here is a model served to a caller holding $0 —
// which is what a model running on our own hardware, or a vendor's free tier, is.
func TestPricedRouteCanCostNothing(t *testing.T) {
	routeDB(t)
	addRoute(t, &object.ModelRoute{
		Owner: "built-in", ModelName: "engine-local", Provider: "engine",
		Upstream: "default", Priced: true, InputPrice: 0, OutputPrice: 0, Enabled: true,
	})

	p, ok := getModelPriceForOrgOK("engine-local", "acme")
	if !ok {
		t.Fatal("a priced route did not answer; free is still inexpressible")
	}
	if p.InputPerMillion != 0 || p.OutputPerMillion != 0 {
		t.Fatalf("price = %v/%v, want 0/0", p.InputPerMillion, p.OutputPerMillion)
	}
	if !costsNothing("engine-local", "acme") {
		t.Fatal("costsNothing = false: the balance gate would refuse a free model")
	}
}

// TestUnpricedZeroRouteStillFallsThrough is the regression that matters more than
// the feature. Every route written before this column exists carries Priced=false
// and two zeroes, and those zeroes have always meant "unstated". If absence read
// as free, every route in the estate — Anthropic, OpenAI, Fireworks — would serve
// paid models for nothing, which is the failure a free FLAG (defaulting false, or
// read off Premium) would have shipped.
func TestUnpricedZeroRouteStillFallsThrough(t *testing.T) {
	routeDB(t)
	addRoute(t, &object.ModelRoute{
		Owner: "built-in", ModelName: "claude-haiku-4-5", Provider: "anthropic",
		Upstream: "claude-haiku-4-5-20251001", Enabled: true, // Priced unset, prices zero
	})

	if costsNothing("claude-haiku-4-5", "acme") {
		t.Fatal("an unstated route read as free — every paid route would stop billing")
	}
}

// TestPricedRouteKeepsANonZeroPrice: the column qualifies the numbers, it does not
// replace them. A route that states a real price is still that price.
func TestPricedRouteKeepsANonZeroPrice(t *testing.T) {
	routeDB(t)
	addRoute(t, &object.ModelRoute{
		Owner: "built-in", ModelName: "priced-model", Provider: "openai-direct",
		Upstream: "gpt-4o", Priced: true, InputPrice: 2.5, OutputPrice: 10, Enabled: true,
	})

	p, ok := getModelPriceForOrgOK("priced-model", "acme")
	if !ok {
		t.Fatal("priced route did not answer")
	}
	if p.InputPerMillion != 2.5 || p.OutputPerMillion != 10 {
		t.Fatalf("price = %v/%v, want 2.5/10", p.InputPerMillion, p.OutputPerMillion)
	}
	if costsNothing("priced-model", "acme") {
		t.Fatal("a route priced above zero read as free")
	}
}

