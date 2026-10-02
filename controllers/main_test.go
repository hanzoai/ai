// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"os"
	"testing"

	"github.com/hanzoai/ai/object"
)

// The paid lane is on for this package's tests, which exercise it; a test of the
// free lane turns it off itself and back on when it ends.
func TestMain(m *testing.M) {
	FreeOnly = func() bool { return false }
	os.Exit(m.Run())
}

// With the paid lane off, a call to any provider but a family's own service is one
// that spends, and is refused; with it on, none is.
func TestPayingIsEveryProviderButAFamilys(t *testing.T) {
	FreeOnly = func() bool { return true }
	t.Cleanup(func() { FreeOnly = func() bool { return false } })
	for typ, want := range map[string]bool{"Zen": false, "Enso": false, "OpenAI": true, "OpenRouter": true, "": true} {
		if got := paying(&object.Provider{Type: typ}); got != want {
			t.Fatalf("paying(%q) = %v, want %v", typ, got, want)
		}
	}
	// Our own speech service pays no vendor; an org's row that borrows its name does.
	if paying(&object.Provider{Owner: "admin", Name: "speech", Type: "OpenAI"}) {
		t.Fatal("the paid lane being off refused our own speech service")
	}
	if !paying(&object.Provider{Owner: "acme", Name: "speech", Type: "OpenAI"}) {
		t.Fatal("an org's provider named speech was taken for ours")
	}
	FreeOnly = func() bool { return false }
	if paying(&object.Provider{Type: "OpenAI"}) {
		t.Fatal("a paid lane that is on refused a priced provider")
	}
}
