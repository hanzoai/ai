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

	// Text is unchanged: usageCostCents' default case delegates to the exact same
	// token calc zapWriteUsage used before, so text rows record an identical cost.
	txt := &usageRecord{Model: "gpt-4o-mini", PromptTokens: 1000, CompletionTokens: 500}
	if got, want := usageCostCents(txt), calculateCostCentsWithCache(txt.Model, txt.PromptTokens, txt.CompletionTokens, txt.CacheReadTokens, txt.CacheWriteTokens); got != want {
		t.Fatalf("text ledger cost changed: got %d, want %d", got, want)
	}
}
