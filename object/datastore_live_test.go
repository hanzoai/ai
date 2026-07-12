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

// TestDatastoreTransportLive exercises the direct datastore transport
// (DatastoreExec/DatastoreQuery — reflect scanning + `?` binding + the cu*
// coercers) against a REAL datastore. It is SKIPPED unless DATASTORE_ADDR is set,
// so normal CI needs no warehouse. It uses an isolated temp table in the default
// database and drops it, so it never touches the hanzo.cloud_usage ledger.
func TestDatastoreTransportLive(t *testing.T) {
	if os.Getenv("DATASTORE_ADDR") == "" {
		t.Skip("DATASTORE_ADDR unset; skipping live datastore transport test")
	}
	InitDatastore()
	deadline := time.Now().Add(15 * time.Second)
	for !DatastoreEnabled() && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if !DatastoreEnabled() {
		t.Fatal("datastore did not connect within 15s")
	}

	ctx := context.Background()
	const tbl = "default.dsverify_transport"
	if err := DatastoreExec(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
		t.Fatalf("drop(pre): %v", err)
	}
	if err := DatastoreExec(ctx, "CREATE TABLE "+tbl+
		" (org String, ts DateTime, toks UInt32, cost UInt64, prem UInt8) ENGINE=MergeTree ORDER BY ts"); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = DatastoreExec(ctx, "DROP TABLE IF EXISTS "+tbl) }()

	now := time.Now().UTC()
	ins := "INSERT INTO " + tbl + " (org, ts, toks, cost, prem) VALUES (?, ?, ?, ?, ?)"
	for _, row := range []struct {
		org  string
		toks int
		cost int
		prem int
	}{{"maxpower", 100, 7, 1}, {"maxpower", 50, 3, 0}, {"other", 999, 999, 1}} {
		if err := DatastoreExec(ctx, ins, row.org, now, row.toks, row.cost, row.prem); err != nil {
			t.Fatalf("insert %s: %v", row.org, err)
		}
	}

	// Per-org aggregate with `?` binding — validates reflect scan over
	// UInt64/UInt32/DateTime/String/UInt8 and the org-scoped WHERE (isolation).
	rows, err := DatastoreQuery(ctx, "SELECT count() AS requests, sum(toks) AS tokens, "+
		"sum(cost) AS cost_cents, max(ts) AS last_ts, any(org) AS org_name, max(prem) AS prem "+
		"FROM "+tbl+" WHERE org = ?", "maxpower")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if got := cuInt64(r["requests"]); got != 2 {
		t.Fatalf("requests: want 2, got %d (per-org isolation broken)", got)
	}
	if got := cuInt64(r["tokens"]); got != 150 {
		t.Fatalf("tokens: want 150, got %d", got)
	}
	if got := cuInt64(r["cost_cents"]); got != 10 {
		t.Fatalf("cost_cents: want 10, got %d", got)
	}
	if got := cuString(r["org_name"]); got != "maxpower" {
		t.Fatalf("org: want maxpower, got %q", got)
	}
	if got := cuBool(r["prem"]); !got {
		t.Fatalf("prem(UInt8): want true")
	}
	if lt := cuTime(r["last_ts"]); lt.IsZero() {
		t.Fatalf("last_ts zero — time.Time scan/coerce broken")
	}
	t.Logf("OK live transport: requests=2 tokens=150 cost=10 org=maxpower last_ts=%s",
		cuTime(r["last_ts"]).Format(time.RFC3339))
}
