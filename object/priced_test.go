// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// A grouped row reads whatever native types the driver decoded.
func TestARoutedRowReadsTheDriversTypes(t *testing.T) {
	r := routed(map[string]any{
		"model": "enso-flash", "served": "openai/gpt-6-luna", "vendor": "openrouter", "status": "success",
		"requests": uint64(3), "uncosted": uint64(1), "losses": uint64(0), "cost": int64(1_392_600),
		"billed": int64(1_980_000), "loss": int64(0), "known": int64(1_320_000),
	})
	want := Routed{
		Model: "enso-flash", Served: "openai/gpt-6-luna", Vendor: "openrouter", Status: "success",
		Requests: 3, Uncosted: 1, CostNano: 1_392_600, BilledNano: 1_980_000, KnownNano: 1_320_000,
	}
	if r != want {
		t.Errorf("row = %+v, want %+v", r, want)
	}
}

// The read dedups by id below its grouping, filters by family and time before the
// dedup, and names the dedup's columns apart from the aggregates' aliases.
func TestTheRoutedReadDedupsBeforeItGroups(t *testing.T) {
	if !strings.Contains(routedQuery, "WHERE provider = ? AND timestamp >= ?) GROUP BY id) GROUP BY model, served, vendor, status") {
		t.Errorf("the read groups before its dedup or filters after it: %s", routedQuery)
	}
	outer, inner, ok := strings.Cut(routedQuery, " FROM (SELECT id, ")
	if !ok {
		t.Fatalf("no dedup in %s", routedQuery)
	}
	inner, _, _ = strings.Cut(inner, " FROM (SELECT * ")
	named := map[string]bool{}
	for _, m := range aliases.FindAllStringSubmatch(inner, -1) {
		named[m[1]] = true
	}
	for _, m := range aliases.FindAllStringSubmatch(outer, -1) {
		if named[m[1]] {
			t.Errorf("an aggregate is named %s, as a column of the dedup is", m[1])
		}
	}
	for _, col := range []string{"served", "vendor", "loss_nano", "cost_nano", "billed_nano", "uncosted"} {
		found := false
		for _, c := range CloudUsageColumns {
			found = found || c == col
		}
		if !found {
			t.Errorf("the ledger writes no %s", col)
		}
	}
}

var aliases = regexp.MustCompile(`AS (\w+)`)

// Without the datastore the read says so rather than answering nothing.
func TestTheRoutedReadNeedsTheLedger(t *testing.T) {
	if DatastoreEnabled() {
		t.Skip("a datastore is connected")
	}
	if _, err := RoutedRows(t.Context(), "enso", time.Now()); err == nil {
		t.Error("a read with no ledger answered")
	}
}
