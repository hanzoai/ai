package controllers

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/ai/object"
	iam "github.com/hanzoai/ai/internal/iam"
)

func TestServingMergesCogsAndCacheTrailers(t *testing.T) {
	h := http.Header{}
	h.Set(providerHeader, "openrouter")
	h.Set(armHeader, "deepseek/deepseek-v4.1-flash")
	sv := servingOf(h)
	if sv.cogs != nil {
		t.Fatalf("expected nil cogs before trailer, got %d", *sv.cogs)
	}

	tr := http.Header{}
	tr.Set(cogsHeader, "0.00425")
	tr.Set(cacheReadHeader, "12000")
	tr.Set(cacheWriteHeader, "500")
	sv.merge(tr)

	if sv.cogs == nil || *sv.cogs != 4_250_000 {
		t.Fatalf("expected cogs 4250000 nano ($0.00425), got %v", sv.cogs)
	}
	if sv.cacheRead != 12000 {
		t.Fatalf("expected cacheRead 12000, got %d", sv.cacheRead)
	}
	if sv.cacheWrite != 500 {
		t.Fatalf("expected cacheWrite 500, got %d", sv.cacheWrite)
	}
}

func TestDecisionRecordCostingOperatedKaiVsResoldJev(t *testing.T) {
	user := &iam.User{Owner: "acme", Name: "alice"}
	kaiProv := &object.Provider{Name: object.KaiName}

	// Operated Kai (no upstream vendor invoice): CostNanoExact = 0, never uncosted.
	kaiRec := decisionRecord(context.Background(), "acme", user, "kai", kaiProv, true, decisionsUsage{
		InputTokens: 1000,
	})
	if kaiRec.CostNanoExact == nil || *kaiRec.CostNanoExact != 0 {
		t.Fatalf("expected operated kai CostNanoExact=0, got %v", kaiRec.CostNanoExact)
	}

	// Resold Jev without stated cost: stays nil (uncosted) so we never fabricate COGS.
	jevUnstated := decisionRecord(context.Background(), "acme", user, "typesafe/jev-1.13", kaiProv, true, decisionsUsage{
		InputTokens: 1000,
	})
	if jevUnstated.CostNanoExact != nil {
		t.Fatalf("expected unstated jev CostNanoExact=nil, got %d", *jevUnstated.CostNanoExact)
	}

	// Resold Jev with stated cost: preserves exact upstream cost.
	c := 0.0012
	jevCosted := decisionRecord(context.Background(), "acme", user, "typesafe/jev-1.13", kaiProv, true, decisionsUsage{
		InputTokens: 1000,
		Cost:        &c,
	})
	if jevCosted.CostNanoExact == nil || *jevCosted.CostNanoExact != 1_200_000 {
		t.Fatalf("expected stated jev CostNanoExact=1200000, got %v", jevCosted.CostNanoExact)
	}
}

func TestZeroDurationAudioRemainsPricedAndCosted(t *testing.T) {
	for _, model := range []string{"zen-scribe", "zen-voice-mini"} {
		rec := &usageRecord{Model: model, Provider: "speech"}
		if recordUnpriced(rec) {
			t.Errorf("%s with 0 duration should not be flagged unpriced", model)
		}
		cost := providerCostNano(rec)
		if cost == nil || *cost != 0 {
			t.Errorf("%s with 0 duration should have operated COGS=0, got %v", model, cost)
		}
	}
}
