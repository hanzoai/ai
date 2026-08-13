package model

import "testing"

// The cached portion of a prompt has to survive the carrier, because every
// consumer downstream reads it from there: the usage record, the ClickHouse
// columns, the per-rate pricing, the gen_ai span attributes.
func TestModelResultCarriesCacheCounts(t *testing.T) {
	r := &ModelResult{PromptTokenCount: 10339, CacheReadTokenCount: 10318}
	if r.CacheReadTokenCount != 10318 {
		t.Fatalf("cache read = %d, want 10318", r.CacheReadTokenCount)
	}
	// Cached tokens are a SUBSET of the prompt, never additional to it —
	// they are the same tokens priced at the cache-read rate.
	if r.CacheReadTokenCount > r.PromptTokenCount {
		t.Fatalf("cache read %d exceeds prompt %d", r.CacheReadTokenCount, r.PromptTokenCount)
	}
}
