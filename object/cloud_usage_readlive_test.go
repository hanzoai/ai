// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License").

package object

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestCloudUsageOverviewLive exercises the REAL read path (GetCloudUsageOverview)
// against the live hanzo.cloud_usage ledger. Read-only. Skipped unless
// DATASTORE_ADDR is set. Prints the maxpower Overview and asserts per-org
// isolation (every activity row is org=maxpower).
func TestCloudUsageOverviewLive(t *testing.T) {
	if os.Getenv("DATASTORE_ADDR") == "" {
		t.Skip("DATASTORE_ADDR unset")
	}
	InitDatastore()
	deadline := time.Now().Add(15 * time.Second)
	for !DatastoreEnabled() && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if !DatastoreEnabled() {
		t.Fatal("datastore did not connect")
	}
	org := os.Getenv("LEDGER_ORG")
	if org == "" {
		org = "maxpower"
	}
	now := time.Now()
	start, end, interval, err := ResolveCloudUsageWindow("24h", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	ov, err := GetCloudUsageOverview(context.Background(), CloudUsageParams{
		RangeLabel: "24h", Start: start, End: end, Interval: interval,
		Org: org, AllOrgs: false, TopModels: 6, ActivityType: "all", ActivityLimit: 20,
	})
	if err != nil {
		t.Fatalf("GetCloudUsageOverview(%s): %v", org, err)
	}
	t.Logf("[%s] totals: requests=%d tokens=%d prompt=%d completion=%d spendCents=%d models=%d providers=%d",
		org, ov.Totals.Requests, ov.Totals.Tokens, ov.Totals.PromptTokens, ov.Totals.CompletionTokens,
		ov.Totals.SpendCents, ov.Totals.Models, ov.Totals.Providers)
	for _, m := range ov.ByModel.Items {
		t.Logf("  by-model: %-18s provider=%-10s requests=%d tokens=%d spendCents=%d pct=%.1f",
			m.Model, m.Provider, m.Requests, m.Tokens, m.SpendCents, m.Pct)
	}
	t.Logf("  activity rows: %d (total=%d)", len(ov.Activity.Items), ov.Activity.Total)
	for i, a := range ov.Activity.Items {
		if i < 5 {
			t.Logf("    %s  %-16s %-9s tok=%d cost=%d org=%s user=%s req=%s",
				a.Time, a.Model, a.Status, a.Tokens, a.CostCents, a.Org, a.User, a.RequestID)
		}
		if a.Org != org {
			t.Fatalf("PER-ORG LEAK: activity row org=%q != %q (req=%s)", a.Org, org, a.RequestID)
		}
	}
	if ov.Totals.Requests == 0 {
		t.Fatalf("no usage rows for org=%s in last 24h — write path not landing", org)
	}
	t.Logf("OK: per-org isolation holds; %d real requests in the %s ledger", ov.Totals.Requests, org)

	// Cross-tenant: the default 'hanzo' org ledger must NOT contain this org's user rows.
	leak, err := DatastoreQuery(context.Background(),
		"SELECT count() AS n FROM hanzo.cloud_usage WHERE organization = 'hanzo' AND user_id LIKE '%davelorenzini%'")
	if err == nil && len(leak) == 1 {
		t.Logf("cross-tenant check: davelorenzini rows in org=hanzo ledger = %d (want 0)", cuInt64(leak[0]["n"]))
	}
}
