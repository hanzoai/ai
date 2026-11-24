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
	"errors"
	"strings"
	"testing"
	"time"
)

func TestThreadStore_CreateAppendForkMergeAndDiskPersistence(t *testing.T) {
	dir := t.TempDir()
	store, err := NewDiskThreadStore(dir)
	if err != nil {
		t.Fatalf("NewDiskThreadStore: %v", err)
	}

	thr, err := store.CreateThread("acme", "alice", "10M refactor", map[string]string{"repo": "hanzo/ai"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if !strings.HasPrefix(thr.ID, "thr_") {
		t.Fatalf("thread ID = %q, want thr_ prefix", thr.ID)
	}

	c1, _, err := store.AppendChunk(thr.ID, "main", nil, Chunk{
		Role:    "system",
		Content: "You are a careful systems engineer.",
		Pinned:  true,
		Actor:   "alice",
		Org:     "acme",
	})
	if err != nil {
		t.Fatalf("append c1: %v", err)
	}
	if !strings.HasPrefix(c1.ID, "chk_") || len(c1.ID) != 20 {
		t.Fatalf("chunk ID = %q, want chk_<16 hex>", c1.ID)
	}

	c2, _, err := store.AppendChunk(thr.ID, "main", nil, Chunk{
		Role:       "user",
		Content:    "Analyze the 10M thread DAG.",
		ResponseID: "resp_turn_1",
		Actor:      "alice",
		Org:        "acme",
	})
	if err != nil {
		t.Fatalf("append c2: %v", err)
	}
	if c2.ParentID != c1.ID {
		t.Fatalf("c2.ParentID = %q, want %q", c2.ParentID, c1.ID)
	}

	// O(1) fork from response_id onto a feature branch.
	forked, err := store.Fork(thr.ID, "main", "resp_turn_1", "exp-1")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forked.HeadChunkID != c2.ID || len(forked.ChunkIDs) != 2 {
		t.Fatalf("forked branch = %+v, want head=%s len=2", forked, c2.ID)
	}

	// Append independent turns to exp-1 and main.
	cExp, _, err := store.AppendChunk(thr.ID, "exp-1", nil, Chunk{
		Role:    "assistant",
		Content: "Branch experiment result: BM25 pager achieves 95% prefix cache hit rate.",
		Actor:   "agent-b",
		Org:     "acme",
		Model:   "enso-ultra",
	})
	if err != nil {
		t.Fatalf("append exp-1: %v", err)
	}
	if cExp.ParentID != c2.ID {
		t.Fatalf("cExp.ParentID = %q, want %q", cExp.ParentID, c2.ID)
	}

	// Merge exp-1 back into main.
	mergeChunk, err := store.Merge(thr.ID, "exp-1", "main", "", "alice", "acme", nil)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if mergeChunk.Kind != "merge" {
		t.Fatalf("merge chunk kind = %q, want merge", mergeChunk.Kind)
	}
	mainChunks, err := store.GetBranchChunks(thr.ID, "main")
	if err != nil {
		t.Fatalf("GetBranchChunks: %v", err)
	}
	if len(mainChunks) != 4 {
		t.Fatalf("main chunks len = %d, want 4 (c1, c2, cExp, merge)", len(mainChunks))
	}

	// Re-open from disk to prove durable zstd blob + JSON metadata persistence.
	reopened, err := NewDiskThreadStore(dir)
	if err != nil {
		t.Fatalf("reopen NewDiskThreadStore: %v", err)
	}
	gotThr, err := reopened.GetThread(thr.ID)
	if err != nil {
		t.Fatalf("reopened GetThread: %v", err)
	}
	if len(gotThr.Branches) != 2 {
		t.Fatalf("reopened branches = %d, want 2", len(gotThr.Branches))
	}
	if tid, br, cid, ok := reopened.LookupResponse("resp_turn_1"); !ok || tid != thr.ID || br != "main" || cid != c2.ID {
		t.Fatalf("reopened LookupResponse = (%q, %q, %q, %v)", tid, br, cid, ok)
	}

	// Lossless byte-range chunk recall.
	slice1, err := reopened.GetChunk(thr.ID, cExp.ID, 0, 17)
	if err != nil {
		t.Fatalf("GetChunk slice1: %v", err)
	}
	if slice1.Content != "Branch experiment" || !slice1.HasMore || slice1.NextOffset != 17 {
		t.Fatalf("slice1 = %+v", slice1)
	}
	slice2, err := reopened.GetChunk(thr.ID, cExp.ID, slice1.NextOffset, 0)
	if err != nil {
		t.Fatalf("GetChunk slice2: %v", err)
	}
	if slice1.Content+slice2.Content != cExp.Content || slice2.HasMore {
		t.Fatalf("reassembled chunk mismatch: %q != %q", slice1.Content+slice2.Content, cExp.Content)
	}
}

func TestThreadStore_FencedLeaseHandoffAndStaleEpochConflict(t *testing.T) {
	store := NewMemoryThreadStore()
	thr, err := store.CreateThread("acme", "alice", "distributed handoff", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Initial epoch is 0. Appending with expectedEpoch=0 succeeds.
	ep0 := int64(0)
	if _, epoch, err := store.AppendChunk(thr.ID, "main", &ep0, Chunk{
		Role: "user", Content: "Start task on laptop-1", Actor: "alice", Org: "acme",
	}); err != nil || epoch != 0 {
		t.Fatalf("append at epoch 0: err=%v epoch=%d", err, epoch)
	}

	// Atomic handoff from epoch 0 -> 1 to a remote agent on gpu-node-7.
	lease1, hChunk, err := store.Handoff(thr.ID, "main", 0, "coder-bot", "partner", "gpu-node-7", "ws://repo", "refs/heads/feat", "enso-ultra", "Take over build verification", 30*time.Minute)
	if err != nil {
		t.Fatalf("Handoff 0->1: %v", err)
	}
	if lease1.Epoch != 1 || lease1.Actor != "coder-bot" || lease1.Org != "partner" || lease1.MachineID != "gpu-node-7" {
		t.Fatalf("lease1 = %+v", lease1)
	}
	if hChunk.Kind != "handoff" || hChunk.Handoff == nil || hChunk.Handoff.FromEpoch != 0 || hChunk.Handoff.ToEpoch != 1 {
		t.Fatalf("handoff chunk = %+v", hChunk)
	}

	// Stale agent attempting to append at epoch 0 MUST fail with ErrLeaseConflict!
	if _, gotEpoch, err := store.AppendChunk(thr.ID, "main", &ep0, Chunk{
		Role: "assistant", Content: "stale write", Actor: "alice", Org: "acme",
	}); !errors.Is(err, ErrLeaseConflict) || gotEpoch != 1 {
		t.Fatalf("stale append = (epoch=%d, err=%v), want ErrLeaseConflict at epoch 1", gotEpoch, err)
	}

	// Stale handoff at epoch 0 MUST also fail with ErrLeaseConflict!
	if _, _, err := store.Handoff(thr.ID, "main", 0, "rogue", "evil", "box", "", "", "", "", time.Minute); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stale handoff err = %v, want ErrLeaseConflict", err)
	}

	// New holder appending at epoch 1 succeeds.
	ep1 := int64(1)
	if _, gotEpoch, err := store.AppendChunk(thr.ID, "main", &ep1, Chunk{
		Role: "assistant", Content: "Verified tests on gpu-node-7", Actor: "coder-bot", Org: "partner",
	}); err != nil || gotEpoch != 1 {
		t.Fatalf("epoch 1 append = (epoch=%d, err=%v)", gotEpoch, err)
	}
}

func TestThreadStore_MultiAccountGrantsAndCanACL(t *testing.T) {
	store := NewMemoryThreadStore()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	store.nowFn = func() time.Time { return now }

	thr, err := store.CreateThread("acme", "alice", "shared incident", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Owner org always allowed, even without explicit grants.
	if !CanAt(thr, "acme", "bob", ThreadScopeRead, now) || !CanAt(thr, "acme", "bob", ThreadScopeHandoff, now) {
		t.Fatal("owner org must always be allowed")
	}

	// Cross-org caller denied by default.
	if CanAt(thr, "partner", "carol", ThreadScopeRead, now) {
		t.Fatal("ungranted cross-org caller must be denied")
	}

	// Grant read+append+handoff to user:partner/carol expiring in 1 hour.
	if _, err := store.AddGrant(thr.ID, Grant{
		Principal: "user:partner/carol",
		Scopes:    []string{ThreadScopeRead, ThreadScopeAppend, ThreadScopeHandoff},
		GrantedBy: "acme/alice",
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("AddGrant user: %v", err)
	}
	// Grant read-only to org:auditors (no expiry).
	if _, err := store.AddGrant(thr.ID, Grant{
		Principal: "org:auditors",
		Scopes:    []string{ThreadScopeRead},
		GrantedBy: "acme/alice",
	}); err != nil {
		t.Fatalf("AddGrant org: %v", err)
	}

	thr, _ = store.GetThread(thr.ID)

	// partner/carol can read, append, handoff, but NOT fork.
	if !CanAt(thr, "partner", "carol", ThreadScopeRead, now) {
		t.Fatal("partner/carol should have read")
	}
	if !CanAt(thr, "partner", "partner/carol", ThreadScopeHandoff, now) {
		t.Fatal("qualified partner/carol should have handoff")
	}
	if CanAt(thr, "partner", "carol", ThreadScopeFork, now) {
		t.Fatal("partner/carol must NOT have fork")
	}
	// Another user in partner (dave) must NOT inherit carol's user grant.
	if CanAt(thr, "partner", "dave", ThreadScopeRead, now) {
		t.Fatal("partner/dave must NOT inherit carol's user grant")
	}
	// Any user in org:auditors can read, cannot append.
	if !CanAt(thr, "auditors", "erin", ThreadScopeRead, now) {
		t.Fatal("auditors/erin should have read via org grant")
	}
	if CanAt(thr, "auditors", "erin", ThreadScopeAppend, now) {
		t.Fatal("auditors/erin must NOT have append")
	}

	// Expired grant MUST be denied.
	afterExpiry := now.Add(2 * time.Hour)
	if CanAt(thr, "partner", "carol", ThreadScopeRead, afterExpiry) {
		t.Fatal("expired grant must be denied")
	}
	// Non-expiring org grant remains valid.
	if !CanAt(thr, "auditors", "erin", ThreadScopeRead, afterExpiry) {
		t.Fatal("non-expiring org grant should remain valid")
	}

	// Revoking org grant immediately denies access.
	if err := store.RevokeGrant(thr.ID, "org:auditors"); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}
	thr, _ = store.GetThread(thr.ID)
	if CanAt(thr, "auditors", "erin", ThreadScopeRead, now) {
		t.Fatal("revoked grant must be denied")
	}
}
