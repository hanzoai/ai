// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import "testing"

// TestLedgerCostAuthority guards the hanzo.cloud_usage analytics/reconciliation
// ledger (written by zapWriteUsage) against the "image/video recorded as $0"
// regression. The ledger MUST price a call with usageCostCents — the SAME authority
// the debit uses — not a token-only calc: image/video calls carry zero tokens, so a
// token-only cost is $0 while the customer is really billed per unit.
func TestLedgerCostAuthority(t *testing.T) {
	// Precondition (why the bug existed): the token-only calc is $0 for a 0-token
	// image call — exactly what zapWriteUsage used to record.
	img := &usageRecord{Model: "gpt-image-1", ImageCount: 2}
	if tokenOnly := calculateCostCentsWithCache(img.Model, img.PromptTokens, img.CompletionTokens, img.CacheReadTokens, img.CacheWriteTokens); tokenOnly != 0 {
		t.Fatalf("precondition: token-only cost of a 0-token image should be 0, got %d", tokenOnly)
	}
	// Fix: the ledger cost authority prices per image, non-zero, == the debit.
	if got, want := usageCostCents(img), imageCostCents(img.Model, 2); got != want || got <= 0 {
		t.Fatalf("image ledger cost: got %d, want %d (>0)", got, want)
	}

	// Video: priced per video, non-zero, == the debit.
	vid := &usageRecord{Model: "wan2-2-t2v-a14b", VideoCount: 1}
	if got, want := usageCostCents(vid), videoCostCents(vid.Model, 1); got != want || got <= 0 {
		t.Fatalf("video ledger cost: got %d, want %d (>0)", got, want)
	}

	// Text is the exact token cost rounded to the cent — the SAME money the nano
	// ledger carries, said less precisely, never a second computation of it.
	txt := &usageRecord{Model: "gpt-4o-mini", PromptTokens: 1000, CompletionTokens: 500}
	if got, want := usageCostCents(txt), nanoToCents(usageCostNano(txt)); got != want {
		t.Fatalf("text ledger cost: got %d, want %d", got, want)
	}

	// And a call worth less than half a cent reports no cents, because it cost none.
	// The old cents path raised it to a whole cent — the same money, fifty times over,
	// in the column every spend view reads. The exact amount is on the same row.
	if cents, nano := usageCostCents(txt), usageCostNano(txt); nano <= 0 || nano >= 5_000_000 {
		t.Fatalf("precondition: this call should cost under half a cent, got %d nano", nano)
	} else if cents != 0 {
		t.Errorf("a %d-nano call reported %d cents; a cent nobody was charged is not a cost", nano, cents)
	}
}
