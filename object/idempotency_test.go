// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hanzoai/dbx"
	kv "github.com/hanzokv/go/v9"
)

func newTestIdemDB(t *testing.T) (*dbx.DB, string) {
	t.Helper()
	dsn := fmt.Sprintf("file:idem_obj_%d?mode=memory&cache=shared&_busy_timeout=5000", time.Now().UnixNano())
	db, err := dbx.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Limit SQLite in-memory pool to keep concurrent write tests deterministic and lock-free.
	db.DB().SetMaxOpenConns(1)
	if err := syncModel(db, &IdempotencyRecord{}); err != nil {
		_ = db.Close()
		t.Fatalf("syncModel IdempotencyRecord: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, "sqlite"
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Verifies length-prefixed framing prevents delimiter collisions across scope
// fields and request fingerprint components.
func TestIdempotencyFramingCollisionFreedom(t *testing.T) {
	s1 := ComputeScopeHash(IdempotencyScope{
		Org: "a:b", Principal: "c", Project: "p", AuthScope: "jwt", Operation: "POST /v1/chat",
	})
	s2 := ComputeScopeHash(IdempotencyScope{
		Org: "a", Principal: "b:c", Project: "p", AuthScope: "jwt", Operation: "POST /v1/chat",
	})
	if s1 == s2 {
		t.Fatalf("delimiter collision in ScopeHash: %s == %s", s1, s2)
	}

	fp1 := ComputeRequestFingerprint("POST", "/v1/a/b", url.Values{"a": {"1"}, "b": {"2"}}, "application/json", []byte(`{"x":1,"y":2}`))
	fp2 := ComputeRequestFingerprint("POST", "/v1/a/b", url.Values{"a": {"1&b=2"}}, "application/json", []byte(`{"x":1,"y":2}`))
	if fp1 == fp2 {
		t.Fatalf("delimiter collision in query RequestFingerprint: %s == %s", fp1, fp2)
	}

	// Semantically identical JSON with reordered keys/whitespace matches.
	fpSame := ComputeRequestFingerprint("POST", "/v1/a/b", url.Values{"b": {"2"}, "a": {"1"}}, "application/json; charset=utf-8", []byte(`{ "y": 2, "x": 1 }`))
	if fp1 != fpSame {
		t.Fatalf("canonical JSON/query fingerprint mismatch: %s != %s", fp1, fpSame)
	}
}

// Verifies state machine transitions, safe CLAIMED takeover, RUNNING -> UNKNOWN
// crash semantics, fencing rejection, reconciliation, and KV eviction durability.
func TestIdempotencyStoreStateMachineAndFencing(t *testing.T) {
	db, drv := newTestIdemDB(t)
	clk := newFakeClock(time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC))

	mr := miniredis.RunT(t)
	kvClient := kv.NewClient(&kv.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = kvClient.Close() })

	cfg := DefaultIdempotencyConfig()
	cfg.LeaseTTL = 10 * time.Second
	cfg.Now = clk.Now

	replicaA := NewIdempotencyStore(db, drv, cfg)
	replicaA.SetKV(NewRedisIdempotencyKV(kvClient))
	replicaB := NewIdempotencyStore(db, drv, cfg)
	replicaB.SetKV(NewRedisIdempotencyKV(kvClient))

	ctx := context.Background()
	scope := IdempotencyScope{
		Org: "acme", Principal: "acme/alice", Project: "prod", AuthScope: "jwt", Operation: "POST /v1/chat/completions",
	}
	fp := ComputeRequestFingerprint("POST", "/v1/chat/completions", nil, "application/json", []byte(`{"model":"zen6"}`))

	// 1) Crash BEFORE inference (CLAIMED) + lease expiry -> safe takeover preserving ExecutionID & BillingRef.
	c1, err := replicaA.Claim(ctx, IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "k-pre-crash", RequestHash: fp, OwnerID: "replica-A"})
	if err != nil || c1.Outcome != ClaimOwned || c1.Record.FenceToken != 1 {
		t.Fatalf("c1 = %+v, err=%v", c1, err)
	}
	// Active lease -> Replica B sees ClaimInFlight.
	c2, err := replicaB.Claim(ctx, IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "k-pre-crash", RequestHash: fp, OwnerID: "replica-B"})
	if err != nil || c2.Outcome != ClaimInFlight {
		t.Fatalf("expected ClaimInFlight while lease active, got %+v err=%v", c2, err)
	}
	// Advance past lease expiry -> Replica B safely takes over CLAIMED with fence=2 and SAME execution_id.
	clk.Advance(11 * time.Second)
	c3, err := replicaB.Claim(ctx, IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "k-pre-crash", RequestHash: fp, OwnerID: "replica-B"})
	if err != nil || c3.Outcome != ClaimOwned || c3.Record.FenceToken != 2 {
		t.Fatalf("expected safe takeover fence=2, got %+v err=%v", c3, err)
	}
	if c3.Record.ExecutionID != c1.Record.ExecutionID || c3.Record.BillingRef != c1.Record.BillingRef {
		t.Fatalf("takeover altered immutable identity: got (%q,%q), want (%q,%q)",
			c3.Record.ExecutionID, c3.Record.BillingRef, c1.Record.ExecutionID, c1.Record.BillingRef)
	}

	// Stale Replica A (fence=1) is fenced on MarkRunning, RenewLease, Complete, Fail.
	if err := replicaA.MarkRunning(ctx, c1.Record.ScopeHash, "k-pre-crash", c1.Record.FenceToken); !errors.Is(err, ErrIdempotencyFenced) {
		t.Fatalf("stale MarkRunning err = %v, want ErrIdempotencyFenced", err)
	}
	if err := replicaA.RenewLease(ctx, c1.Record.ScopeHash, "k-pre-crash", c1.Record.FenceToken); !errors.Is(err, ErrIdempotencyFenced) {
		t.Fatalf("stale RenewLease err = %v, want ErrIdempotencyFenced", err)
	}
	if err := replicaA.Complete(ctx, c1.Record.ScopeHash, "k-pre-crash", c1.Record.FenceToken, IdempotencyOutcome{Status: 200, Body: []byte("stale")}); !errors.Is(err, ErrIdempotencyFenced) {
		t.Fatalf("stale Complete err = %v, want ErrIdempotencyFenced", err)
	}

	// Authorized Replica B (fence=2) runs and commits.
	if err := replicaB.MarkRunning(ctx, c3.Record.ScopeHash, "k-pre-crash", c3.Record.FenceToken); err != nil {
		t.Fatalf("replicaB MarkRunning: %v", err)
	}
	if err := replicaB.Complete(ctx, c3.Record.ScopeHash, "k-pre-crash", c3.Record.FenceToken, IdempotencyOutcome{
		Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(`{"ok":"from-B"}`),
	}); err != nil {
		t.Fatalf("replicaB Complete: %v", err)
	}

	// Flush KV + L1 caches -> still replays from authoritative SQL.
	mr.FlushAll()
	replicaA.EvictCaches()
	replicaB.EvictCaches()
	replay, err := replicaA.Claim(ctx, IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "k-pre-crash", RequestHash: fp, OwnerID: "replica-A"})
	if err != nil || replay.Outcome != ClaimCompleted || string(replay.Record.ResponseBody) != `{"ok":"from-B"}` {
		t.Fatalf("post-eviction replay = %+v err=%v", replay, err)
	}

	// 2) Crash AFTER MarkRunning (RUNNING) + lease expiry -> transitions to UNKNOWN, never re-executes, reconciles cleanly.
	r1, err := replicaA.Claim(ctx, IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "k-post-crash", RequestHash: fp, OwnerID: "replica-A"})
	if err != nil || r1.Outcome != ClaimOwned {
		t.Fatalf("r1 = %+v err=%v", r1, err)
	}
	if err := replicaA.MarkRunning(ctx, r1.Record.ScopeHash, "k-post-crash", r1.Record.FenceToken); err != nil {
		t.Fatalf("r1 MarkRunning: %v", err)
	}
	clk.Advance(11 * time.Second)
	r2, err := replicaB.Claim(ctx, IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "k-post-crash", RequestHash: fp, OwnerID: "replica-B"})
	if err != nil || r2.Outcome != ClaimUnknown || r2.Record.State != IdemStateUnknown {
		t.Fatalf("expected ClaimUnknown after RUNNING lease expiry, got %+v err=%v", r2, err)
	}
	// Stale Replica A trying to complete after RUNNING->UNKNOWN is fenced!
	if err := replicaA.Complete(ctx, r1.Record.ScopeHash, "k-post-crash", r1.Record.FenceToken, IdempotencyOutcome{Status: 200, Body: []byte("late")}); !errors.Is(err, ErrIdempotencyFenced) {
		t.Fatalf("stale Complete after UNKNOWN err = %v, want ErrIdempotencyFenced", err)
	}
	// Explicit reconciliation resolves UNKNOWN -> COMPLETED.
	if err := replicaB.Reconcile(ctx, r2.Record.ScopeHash, "k-post-crash", r2.Record.FenceToken, IdemStateCompleted, IdempotencyOutcome{
		Status: 200, Body: []byte(`{"reconciled":true}`), SideEffectsCommitted: true,
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	r3, err := replicaA.Claim(ctx, IdempotencyClaimRequest{Scope: scope, IdempotencyKey: "k-post-crash", RequestHash: fp, OwnerID: "replica-A"})
	if err != nil || r3.Outcome != ClaimCompleted || string(r3.Record.ResponseBody) != `{"reconciled":true}` {
		t.Fatalf("post-reconcile replay = %+v err=%v", r3, err)
	}
}
