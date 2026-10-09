// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"os"
	"testing"

	"github.com/hanzoai/ai/object"
)

// The paid lane is on for this package's tests, which exercise it, and every request
// they build is seated on it (visit, paidSeat); a test of the free lane closes it
// itself and opens it again when it ends, and a test of the lanes themselves
// (lane_test.go) seats each caller.
//
// The vendor accounts are the ones a test sets, never one the shell running the suite
// holds: a real key in the environment is one more account the pool walks, which a
// test vendor benches on its first refusal.
func TestMain(m *testing.M) {
	FreeOnly = func() bool { return false }
	for _, k := range object.OpenRouterKeys {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}

// With the paid lane off, a call to any provider but a family's own service is one
// that spends, and is refused; with it on, none is.
func TestPayingIsEveryProviderButAFamilys(t *testing.T) {
	ctx := paidSeat(context.Background())
	FreeOnly = func() bool { return true }
	t.Cleanup(func() { FreeOnly = func() bool { return false } })
	for typ, want := range map[string]bool{"Zen": false, "Enso": false, "OpenAI": true, "OpenRouter": true, "": true} {
		if got := paying(ctx, &object.Provider{Type: typ}); got != want {
			t.Fatalf("paying(%q) = %v, want %v", typ, got, want)
		}
	}
	// Our own speech service pays no vendor; an org's row that borrows its name does.
	if paying(ctx, &object.Provider{Owner: "admin", Name: "speech", Type: "OpenAI"}) {
		t.Fatal("the paid lane being off refused our own speech service")
	}
	if !paying(ctx, &object.Provider{Owner: "acme", Name: "speech", Type: "OpenAI"}) {
		t.Fatal("an org's provider named speech was taken for ours")
	}
	FreeOnly = func() bool { return false }
	if paying(ctx, &object.Provider{Type: "OpenAI"}) {
		t.Fatal("a paid lane that is on refused a priced provider")
	}
}
