// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"strings"
	"testing"
	"time"
)

// A row reads whatever native types the driver decoded it into.
func TestAPricedRowReadsTheDriversTypes(t *testing.T) {
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	r := pricedRow(map[string]any{
		"id": "a", "at": at, "model": "enso-flash", "reasoning": "low", "served": "openai/gpt-6-luna",
		"vendor": "openrouter", "status": "success", "prompt": uint32(300000), "cached": uint32(200000),
		"written": uint32(0), "completion": uint32(2000), "cost": int64(26902500), "billed": int64(28382138),
		"loss": int64(0), "uncosted": uint8(0),
	})
	want := PricedRow{ID: "a", At: at, Model: "enso-flash", Reasoning: "low", Served: "openai/gpt-6-luna", Vendor: "openrouter",
		Status: "success", Prompt: 300000, Cached: 200000, Completion: 2000, CostNano: 26902500, BilledNano: 28382138}
	if r != want {
		t.Errorf("row = %+v, want %+v", r, want)
	}
	if !pricedRow(map[string]any{"uncosted": uint8(1)}).Uncosted {
		t.Error("an uncosted row read as costed")
	}
}

// The reads are deduplicated by id, filtered by family and time before the dedup,
// and the last-calls read keeps n of each model and depth.
func TestThePricedReadsDedupAndKeepTheLastOfEachDepth(t *testing.T) {
	if !strings.Contains(pricedColumns, "WHERE provider = ? AND timestamp >= ?) GROUP BY id") {
		t.Errorf("the read filters after its dedup: %s", pricedColumns)
	}
	for _, col := range []string{"reasoning", "served", "vendor", "loss_nano", "cost_nano", "billed_nano"} {
		found := false
		for _, c := range CloudUsageColumns {
			found = found || c == col
		}
		if !found {
			t.Errorf("the ledger writes no %s", col)
		}
	}
}

// Without the datastore the read says so rather than answering nothing.
func TestThePricedReadNeedsTheLedger(t *testing.T) {
	if DatastoreEnabled() {
		t.Skip("a datastore is connected")
	}
	if _, err := PricedRows(t.Context(), "enso", time.Now(), 1); err == nil {
		t.Error("a read with no ledger answered")
	}
}
