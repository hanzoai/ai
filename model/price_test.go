// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package model

import (
	"math"
	"testing"
)

// A price is carried as a float and accumulated one completion at a time, so what
// stops it drifting is that every step rounds. Without that, a thousand small
// charges do not add up to what they are.
func TestPricesAddUpToWhatTheyAre(t *testing.T) {
	// The canonical float surprise, and what a running total must not carry.
	if got := AddPrices(0.1, 0.2); got != 0.3 {
		t.Errorf("0.1 + 0.2 = %v, want 0.3", got)
	}

	// A thousand completions at a tenth of a cent each is one dollar.
	total := 0.0
	for i := 0; i < 1000; i++ {
		total = AddPrices(total, 0.001)
	}
	if total != 1.0 {
		t.Errorf("a thousand charges of $0.001 came to %v", total)
	}

	// Down to the place the ledger keeps: a hundred million steps of a nano would
	// be slow to run, so take one step of each size and check it survives.
	for _, p := range []float64{1e-8, 1e-7, 1e-6, 0.00132, 12.34567891} {
		if got := AddPrices(0, p); got != math.Round(p*1e8)/1e8 {
			t.Errorf("AddPrices(0, %v) = %v", p, got)
		}
	}
	// And a step below what is kept rounds away rather than accumulating noise.
	if got := AddPrices(0, 1e-12); got != 0 {
		t.Errorf("a price below what is kept came to %v", got)
	}
}

// A completion's price is its token count against a per-thousand rate, and the
// rate is the number a vendor publishes.
func TestWhatACompletionCosts(t *testing.T) {
	for _, c := range []struct {
		tokens int
		rate   float64
		want   float64
	}{
		{1000, 0.002, 0.002},
		{500, 0.002, 0.001},
		{1, 0.002, 0.000002},
		{0, 0.002, 0},
		{1_000_000, 0.03, 30},
		{333, 0.003, 0.000999},
	} {
		if got := getPrice(c.tokens, c.rate); got != c.want {
			t.Errorf("%d tokens at $%v/1k = %v, want %v", c.tokens, c.rate, got, c.want)
		}
	}

	// A rate nobody set is no charge, not a negative one.
	if got := getPrice(1000, 0); got != 0 {
		t.Errorf("an unpriced completion cost %v", got)
	}
}

// RefinePrice is what a person is shown, so it is cents.
func TestWhatAPersonIsShown(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0.004, 0.0},
		{0.005, 0.01},
		{0.006, 0.01},
		{1.235, 1.24},
		{12.3456789, 12.35},
		{0, 0},
	} {
		if got := RefinePrice(c.in); got != c.want {
			t.Errorf("RefinePrice(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
