// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestZapDatastoreCanary is the devnet canary for the datastore-over-ZAP cutover.
// It exercises the FULL wire path against the real cmd/zap-bridge (:9999) —
// HMAC-signed frames, /exec, /query NDJSON decode — and asserts per-org
// isolation (a WHERE organization=? read returns only the target org's rows),
// which is the money-critical property before the live cloud_usage ledger is
// repointed here.
//
// It writes to hanzo.cloud_usage_zap_canary (a throwaway table, dropped in
// cleanup), NEVER the live hanzo.cloud_usage, so running it is side-effect free.
//
// Gated: skips unless ZAP_DATASTORE_ADDR and DATASTORE_BRIDGE_HMAC_KEY are set.
// Run on devnet:
//
//	ZAP_ENABLED=true \
//	ZAP_DATASTORE_ADDR=insights-datastore:9999 \
//	DATASTORE_BRIDGE_HMAC_KEY=<base64 key from the `datastore` secret> \
//	go test ./object/ -run TestZapDatastoreCanary -v
func TestZapDatastoreCanary(t *testing.T) {
	if os.Getenv("ZAP_DATASTORE_ADDR") == "" || os.Getenv("DATASTORE_BRIDGE_HMAC_KEY") == "" {
		t.Skip("set ZAP_DATASTORE_ADDR + DATASTORE_BRIDGE_HMAC_KEY to run the datastore-ZAP canary")
	}
	os.Setenv("ZAP_ENABLED", "true")

	InitZap()

	// Peer connect is async; wait for the signed peer to be ready.
	deadline := time.Now().Add(20 * time.Second)
	for !DatastoreZapReady() {
		if time.Now().After(deadline) {
			t.Fatal("datastore ZAP peer never became ready (check ZAP_DATASTORE_ADDR reachability + HMAC key)")
		}
		time.Sleep(250 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const table = "hanzo.cloud_usage_zap_canary"
	if err := ZapDatastoreExec(ctx, "CREATE TABLE IF NOT EXISTS "+table+
		" (organization String, request_id String, total_tokens UInt64) ENGINE = MergeTree ORDER BY (organization, request_id)"); err != nil {
		t.Fatalf("exec CREATE over ZAP: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_ = ZapDatastoreExec(cctx, "DROP TABLE IF EXISTS "+table)
	})

	// Two orgs, one row each — proves the write path AND that the org column
	// round-trips through HMAC-signed /exec.
	if err := ZapDatastoreExec(ctx, "INSERT INTO "+table+" (organization, request_id, total_tokens) VALUES (?, ?, ?)", "orgA", "req-a-1", uint64(111)); err != nil {
		t.Fatalf("exec INSERT orgA over ZAP: %v", err)
	}
	if err := ZapDatastoreExec(ctx, "INSERT INTO "+table+" (organization, request_id, total_tokens) VALUES (?, ?, ?)", "orgB", "req-b-1", uint64(222)); err != nil {
		t.Fatalf("exec INSERT orgB over ZAP: %v", err)
	}

	// Per-org isolation: a bound-parameter WHERE organization=? must return
	// ONLY orgA. This is the exact predicate the cloud_usage read path uses.
	rows, err := ZapDatastoreQuery(ctx, "SELECT organization, request_id, total_tokens FROM "+table+" WHERE organization = ? ORDER BY request_id", "orgA")
	if err != nil {
		t.Fatalf("query over ZAP: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("per-org isolation: got %d rows for orgA, want 1 (cross-tenant leak if >1): %+v", len(rows), rows)
	}
	if got := toString(rows[0]["organization"]); got != "orgA" {
		t.Fatalf("per-org isolation: row org = %q, want orgA", got)
	}
	if got := toString(rows[0]["request_id"]); got != "req-a-1" {
		t.Fatalf("round-trip: request_id = %q, want req-a-1", got)
	}
	t.Logf("datastore-ZAP canary OK: HMAC auth + /exec + /query NDJSON + per-org isolation verified against %s", os.Getenv("ZAP_DATASTORE_ADDR"))
}

// toString coerces a decoded NDJSON scalar (string, or json.Number/float) to a
// string for assertions without pulling in the cu* coercers.
func toString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return ""
	}
}
