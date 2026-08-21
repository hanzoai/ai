// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// useCatalog loads the catalog at path and restores whatever the package held when
// the test ends. It is how this package loads one, and the restore is why.
//
// The config is a process-wide singleton the listing is a function of, so a test
// that loads one and walks away has changed the answer for everything after it.
// That is not hypothetical: the two tests covering the STATIC table — the fallback
// consulted only when NO catalog is loaded — found one loaded and skipped
// themselves. In file order the load came first every time, so neither had run in
// however long, and running either on its own showed it failing.
func useCatalog(tb testing.TB, path string) {
	tb.Helper()
	prev := globalModelConfig
	tb.Cleanup(func() { globalModelConfig = prev })
	if err := InitModelConfig(path); err != nil {
		tb.Fatalf("load %s: %v", path, err)
	}
}

// withCatalog loads a config naming exactly the given models.
func withCatalog(t *testing.T, models ...string) {
	t.Helper()
	yaml := "version: 1\nmodels:\n"
	for _, m := range models {
		yaml += fmt.Sprintf("  %s:\n    provider: do-ai\n    upstream: %s\n", m, m)
	}
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	useCatalog(t, path)
}

func lists(t *testing.T, id string) bool {
	t.Helper()
	for _, m := range listAvailableModels() {
		if m.ID == id {
			return true
		}
	}
	return false
}

// The catalogue is built when its inputs move, not when a caller asks. Two requests
// arriving between two builds are handed the same bytes — the same array, so the
// second costs the wire and nothing else.
//
// Building per caller is what made this the slowest route in the process: 534 models
// merged, sorted and marshalled, and one provider lookup per model to resolve the
// window it is served at.
func TestListingIsHeldBetweenBuilds(t *testing.T) {
	withCatalog(t, "probe-held")

	first, err := modelListing(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("empty listing")
	}
	second, err := modelListing(nil)
	if err != nil {
		t.Fatal(err)
	}
	if &first[0] != &second[0] {
		t.Error("the catalogue was rebuilt for the second caller; nothing it is built from had changed")
	}
}

// Held is not frozen: a config that changes is a catalogue that changes, on the very
// next request.
func TestListingFollowsTheConfig(t *testing.T) {
	withCatalog(t, "probe-before")
	if !lists(t, "probe-before") {
		t.Fatal("configured model missing from the listing")
	}

	withCatalog(t, "probe-after")
	if lists(t, "probe-before") {
		t.Error("a model the config no longer names is still listed")
	}
	if !lists(t, "probe-after") {
		t.Error("a model the config now names is not listed")
	}
}

// The listing handed to a caller is theirs. annotateModelAccess writes each gated
// SKU's standing into it, so a caller given the held value would be writing into what
// the next caller reads — Access included, which is the one field that says something
// about the reader rather than about the model.
func TestListingIsPerCaller(t *testing.T) {
	withCatalog(t, "probe-mine")

	// A gated SKU is the case that matters and a family serves none here, so seed
	// one onto the held catalogue.
	if _, _, err := listing.get(); err != nil {
		t.Fatal(err)
	}
	listing.mu.Lock()
	listing.models[0].Access = &modelAccessInfo{State: "waitlist"}
	listing.mu.Unlock()

	mine, theirs := listAvailableModels(), listAvailableModels()
	if mine[0].Access == theirs[0].Access {
		t.Fatal("two callers share one Access; whichever is annotated last decides what both are told")
	}

	mine[0].ID = "probe-written"
	mine[0].Access.State = "granted"
	if theirs[0].ID == "probe-written" || theirs[0].Access.State != "waitlist" {
		t.Error("one caller's listing reached another's")
	}

	body, err := modelListing(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("probe-written")) || bytes.Contains(body, []byte("granted")) {
		t.Error("the public body carries a caller's own edits")
	}
}
