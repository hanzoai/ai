// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

// Hermetic tests for the cloud memory backend. They spin up an in-memory SQLite
// store (no external DB, no embedding provider), so they run under the CI
// `skipCi` tag alongside the other pure unit tests. The headline test proves the
// security invariant: a caller can never read/update/delete another user's
// memory, even within the same org and even knowing the exact memory name.
package object

import (
	"fmt"
	"sync/atomic"
	"testing"
)

var memTestSeq int64

// useMemoryTestDB points the package adapter at a fresh in-memory SQLite DB and
// restores the previous adapter on cleanup.
func useMemoryTestDB(t *testing.T) {
	t.Helper()
	n := atomic.AddInt64(&memTestSeq, 1)
	dsn := fmt.Sprintf("file:memtest_%d?mode=memory&cache=shared", n)
	// Sync the tables the memory paths touch. Store/Provider are present (empty)
	// so the best-effort embedding lookup resolves cleanly to "no provider".
	restore, err := UseMemoryDB(dsn, &Memory{}, &Store{}, &Provider{})
	if err != nil {
		t.Fatalf("useMemoryTestDB: %v", err)
	}
	t.Cleanup(restore)
}

func mustAddMemory(t *testing.T, owner, userID, kind, content string) *Memory {
	t.Helper()
	m := &Memory{Owner: owner, UserId: userID, Kind: kind, Content: content}
	ok, err := AddMemory(m)
	if err != nil || !ok {
		t.Fatalf("AddMemory(%s/%s) failed: ok=%v err=%v", owner, userID, ok, err)
	}
	return m
}

// TestMemoryCrossUserReadDenied is the security gate: alice and bob share an org
// but bob must never reach alice's memory through any accessor.
func TestMemoryCrossUserReadDenied(t *testing.T) {
	useMemoryTestDB(t)

	const org = "hanzo"
	alice := mustAddMemory(t, org, "alice", MemoryKindUser, "alice's private API key is sk-alice-secret")
	bob := mustAddMemory(t, org, "bob", MemoryKindUser, "bob likes hiking")

	// 1. Direct scoped fetch by the exact name is denied for the wrong user.
	if got, err := GetMemoryScoped(org, "bob", alice.Name); err != nil || got != nil {
		t.Fatalf("bob read alice's memory by name: got=%v err=%v (must be nil)", got, err)
	}
	// Sanity: the rightful owner CAN read it.
	if got, err := GetMemoryScoped(org, "alice", alice.Name); err != nil || got == nil {
		t.Fatalf("alice could not read her own memory: got=%v err=%v", got, err)
	}

	// 2. Fetch-by-id (owner/name) is denied across users and across orgs.
	if got, _ := GetMemoryByIdScoped(org, "bob", alice.GetId()); got != nil {
		t.Fatalf("bob read alice's memory by id %q", alice.GetId())
	}
	if got, _ := GetMemoryByIdScoped("other-org", "alice", alice.GetId()); got != nil {
		t.Fatalf("cross-org read of alice's memory succeeded")
	}

	// 3. Listing is scoped — bob never sees alice's rows.
	bobList, err := GetMemories(org, "bob")
	if err != nil {
		t.Fatalf("GetMemories(bob): %v", err)
	}
	if len(bobList) != 1 || bobList[0].Name != bob.Name {
		t.Fatalf("bob's list leaked rows: %+v", bobList)
	}

	// 4. Search is scoped — querying alice's secret text as bob returns nothing.
	hits, err := SearchMemories(org, "bob", "sk-alice-secret", "", 10, "en")
	if err != nil {
		t.Fatalf("SearchMemories(bob): %v", err)
	}
	for _, h := range hits {
		if h.UserId != "bob" {
			t.Fatalf("search leaked %s's memory to bob: %+v", h.UserId, h)
		}
	}
	if len(hits) != 0 {
		t.Fatalf("bob's search for alice's secret returned %d hits, want 0", len(hits))
	}

	// 5. Delete is scoped — bob cannot delete alice's memory; it survives.
	if affected, _ := DeleteMemoryScoped(org, "bob", alice.Name); affected {
		t.Fatalf("bob deleted alice's memory")
	}
	if got, _ := GetMemoryScoped(org, "alice", alice.Name); got == nil {
		t.Fatalf("alice's memory disappeared after bob's delete attempt")
	}

	// 6. Update is scoped — bob cannot overwrite alice's memory.
	if affected, _ := UpdateMemoryScoped(org, "bob", alice.Name, &Memory{Content: "pwned"}, "en"); affected {
		t.Fatalf("bob updated alice's memory")
	}
	got, _ := GetMemoryScoped(org, "alice", alice.Name)
	if got == nil || got.Content == "pwned" {
		t.Fatalf("alice's content was modified by bob: %+v", got)
	}
}

// TestMemoryRememberAndTextSearch exercises the functional happy path with the
// text-search fallback (no embedding provider configured in the test DB).
func TestMemoryRememberAndTextSearch(t *testing.T) {
	useMemoryTestDB(t)

	const org, user = "hanzo", "alice"
	mustAddMemory(t, org, user, MemoryKindUser, "I love the Go programming language")
	mustAddMemory(t, org, user, MemoryKindUser, "Python is acceptable for scripting")

	hits, err := SearchMemories(org, user, "Go programming", "", 10, "en")
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(hits) != 1 || hits[0].Content != "I love the Go programming language" {
		t.Fatalf("text search returned wrong result: %+v", hits)
	}

	all, err := GetMemories(org, user)
	if err != nil || len(all) != 2 {
		t.Fatalf("GetMemories: len=%d err=%v", len(all), err)
	}
}

// TestMemoryFactsFiltersByKind proves /facts only returns fact-kind memories.
func TestMemoryFactsFiltersByKind(t *testing.T) {
	useMemoryTestDB(t)

	const org, user = "hanzo", "alice"
	mustAddMemory(t, org, user, MemoryKindUser, "a user preference")
	fact := mustAddMemory(t, org, user, MemoryKindFact, "the sky is blue")

	facts, err := GetFacts(org, user, 50)
	if err != nil {
		t.Fatalf("GetFacts: %v", err)
	}
	if len(facts) != 1 || facts[0].Name != fact.Name {
		t.Fatalf("GetFacts returned wrong set: %+v", facts)
	}
}

// TestMemoryUpdateAndDeleteScopedToOwner confirms the rightful owner can mutate
// and remove their own memory.
func TestMemoryUpdateAndDeleteScopedToOwner(t *testing.T) {
	useMemoryTestDB(t)

	const org, user = "hanzo", "alice"
	m := mustAddMemory(t, org, user, MemoryKindUser, "original content")

	ok, err := UpdateMemoryScoped(org, user, m.Name, &Memory{Content: "updated content", Kind: MemoryKindProject}, "en")
	if err != nil || !ok {
		t.Fatalf("owner update failed: ok=%v err=%v", ok, err)
	}
	got, _ := GetMemoryScoped(org, user, m.Name)
	if got == nil || got.Content != "updated content" || got.Kind != MemoryKindProject {
		t.Fatalf("update not applied: %+v", got)
	}

	ok, err = DeleteMemoryScoped(org, user, m.Name)
	if err != nil || !ok {
		t.Fatalf("owner delete failed: ok=%v err=%v", ok, err)
	}
	if got, _ := GetMemoryScoped(org, user, m.Name); got != nil {
		t.Fatalf("memory still present after delete: %+v", got)
	}
}

// TestNormalizeMemoryKind covers the kind taxonomy normalization.
func TestNormalizeMemoryKind(t *testing.T) {
	cases := map[string]string{
		"user":      MemoryKindUser,
		"FACT":      MemoryKindFact,
		"  project": MemoryKindProject,
		"reference": MemoryKindReference,
		"feedback":  MemoryKindFeedback,
		"":          MemoryKindUser,
		"garbage":   MemoryKindUser,
	}
	for in, want := range cases {
		if got := NormalizeMemoryKind(in); got != want {
			t.Errorf("NormalizeMemoryKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every memory accessor reads through the adapter, and the adapter is nil
// before the DB is initialised. Unguarded, the read is a nil dereference rather
// than an answer: GET /v1/ai/memory/list panicked in listMemoriesScoped and the
// recover turned it into a 500 whose body named no cause, on a route a chat
// client calls to draw a panel. Each accessor asks `store()` first, so an
// uninitialised store is an error the caller can report.
func TestMemoryWithoutAStoreAnswersAnErrorRatherThanCrash(t *testing.T) {
	saved := adapter
	adapter = nil
	t.Cleanup(func() { adapter = saved })

	if _, err := GetMemories("hanzo", "z"); err == nil {
		t.Error("GetMemories answered no error with no store behind it")
	}
	if _, err := RecallMemories("hanzo", "z", "", 20); err == nil {
		t.Error("RecallMemories answered no error with no store behind it")
	}
	if _, err := CountMemories("hanzo", "z"); err == nil {
		t.Error("CountMemories answered no error with no store behind it")
	}
	if _, err := DeleteMemoryScoped("hanzo", "z", "one"); err == nil {
		t.Error("DeleteMemoryScoped answered no error with no store behind it")
	}
}
