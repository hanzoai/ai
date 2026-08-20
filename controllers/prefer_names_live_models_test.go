// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// retiredFamilies are model families collapsed into the zen5 lineup. An id from
// one can never be served, by anything, ever again.
var retiredFamilies = []string{"zen4", "zen3", "zen2"}

// The router's preference lists name model ids, and a dead id is INVISIBLE:
// "first servable wins" means the router walks past a dead champion and answers
// from a fallback, so the only symptom is a preference that quietly does
// nothing. Nothing anywhere reported it.
//
// It had gone wrong in every list at once. All eight led with a retired id, and
// `vision` had its top THREE dead with the live house vision SKU absent
// altogether — so the one task where the house model matters most was served
// entirely by fallbacks.
//
// This asserts the half that is provable OFFLINE and was the actual regression:
// no preference may name a retired family. It deliberately does NOT assert that
// every id is declared in conf/models.yaml, because that is not true and should
// not be — prod's model surface is the deployed ConfigMap plus what the zen,
// enso and openrouter families discover at runtime, and this file is only one
// of those. Asserting it would fail on `gpt-5.3-codex`, which is servable today.
// Catching a live-but-undeclared id is `cmd/catalog`'s job, against the vendor;
// catching a resurrected zen4 is this one's, and needs no network.
func TestPreferNamesNoRetiredFamily(t *testing.T) {
	prefer := loadPrefer(t)
	for task, ids := range prefer {
		if len(ids) == 0 {
			t.Errorf("prefer[%q] is empty", task)
			continue
		}
		for i, id := range ids {
			for _, dead := range retiredFamilies {
				if strings.HasPrefix(id, dead) {
					t.Errorf("prefer[%q][%d] = %q — %s* is retired and can never serve", task, i, id, dead)
				}
			}
		}
	}
}

// The FIRST entry is the champion — the model the list exists to prefer. This is
// the position that was wrong in all eight lists, and the position where being
// wrong is quietest, because a fallback answers and the request succeeds.
func TestEveryPreferListLeadsWithALiveChampion(t *testing.T) {
	for task, ids := range loadPrefer(t) {
		if len(ids) == 0 {
			continue
		}
		for _, dead := range retiredFamilies {
			if strings.HasPrefix(ids[0], dead) {
				t.Errorf("prefer[%q] leads with %q — the champion never serves and a fallback answers silently", task, ids[0])
			}
		}
	}
}

// Every task tag carries a house model, and it leads. A list of nothing but
// other vendors' models is a task we route away by default — `vision` was
// exactly that once its three zen entries died.
func TestEveryPreferListLeadsWithAHouseModel(t *testing.T) {
	for task, ids := range loadPrefer(t) {
		if len(ids) == 0 {
			continue
		}
		if !strings.HasPrefix(ids[0], "zen") && !strings.HasPrefix(ids[0], "enso") {
			t.Errorf("prefer[%q] leads with %q, not a house model — the default experience routes to another vendor", task, ids[0])
		}
	}
}

func loadPrefer(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile("../conf/models.yaml")
	if err != nil {
		t.Fatalf("read conf/models.yaml: %v", err)
	}
	var doc struct {
		Router struct {
			Prefer map[string][]string `yaml:"prefer"`
		} `yaml:"router"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse conf/models.yaml: %v", err)
	}
	if len(doc.Router.Prefer) == 0 {
		t.Fatal("no router.prefer lists found — the check would pass vacuously")
	}
	return doc.Router.Prefer
}
