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
	"fmt"
	"strings"
	"testing"
)

func TestProjectWindow_10MTokenProjectionPrefixStabilityAndColdRecall(t *testing.T) {
	// Build a synthetic 10,000,000+ token thread across 2,000 chunks (5,000 tokens each)
	// plus a cache-stable system prefix, an explicitly pinned chunk, and a needle chunk
	// buried deep in the cold region.
	chunks := make([]Chunk, 0, 2010)
	sysChunk := Chunk{
		ID:      "chk_0000000000000001",
		Role:    "system",
		Content: "System policy: preserve deterministic prefix byte-for-byte.",
		Tokens:  120,
	}
	devChunk := Chunk{
		ID:      "chk_0000000000000002",
		Role:    "developer",
		Content: "Developer constraints: never drop tool pairs.",
		Tokens:  80,
	}
	chunks = append(chunks, sysChunk, devChunk)

	needleID := "chk_needle0000000042"
	for i := 0; i < 2005; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		c := Chunk{
			ID:      fmt.Sprintf("chk_%016x", i+10),
			Role:    role,
			Content: fmt.Sprintf("Turn %d routine build log output and repository telemetry metrics.", i),
			Tokens:  5_000,
		}
		if i == 50 {
			c.ID = "chk_pinned0000000050"
			c.Pinned = true
			c.Content = "PINNED ARCHITECTURE SPEC: 3-tier lossless context pager."
			c.Tokens = 100
		}
		if i == 420 {
			c.ID = needleID
			c.Content = "CRITICAL ROOT CAUSE: quantum_flux_capacitor_9991 overflow in allocator ring buffer."
			c.Tokens = 200
		}
		chunks = append(chunks, c)
	}

	// Recent hot tail turns (small tokens so multiple fit verbatim).
	for i := 0; i < 4; i++ {
		chunks = append(chunks, Chunk{
			ID:      fmt.Sprintf("chk_hot000000000000%d", i),
			Role:    "user",
			Content: fmt.Sprintf("Recent hot turn %d", i),
			Tokens:  50,
		})
	}

	proj1 := ProjectWindow(chunks, 8_000, "quantum_flux_capacitor_9991 allocator ring buffer")
	if proj1.TotalSourceTokens < 10_000_000 {
		t.Fatalf("TotalSourceTokens = %d, want >= 10,000,000", proj1.TotalSourceTokens)
	}
	if proj1.ProjectedTokens > 8_000 {
		t.Fatalf("ProjectedTokens = %d, exceeded target 8000", proj1.ProjectedTokens)
	}
	if proj1.PinnedCount != 3 {
		t.Fatalf("PinnedCount = %d, want 3 (system + developer + pinned)", proj1.PinnedCount)
	}
	if proj1.RecalledCount < 1 {
		t.Fatalf("RecalledCount = %d, want >= 1 (needle chunk auto-recalled)", proj1.RecalledCount)
	}
	if proj1.ColdHandleCount < 1900 {
		t.Fatalf("ColdHandleCount = %d, want >= 1900 cold handles", proj1.ColdHandleCount)
	}

	// Verify the buried needle was auto-paged in verbatim.
	foundNeedle := false
	foundColdIndex := false
	for _, c := range proj1.Chunks {
		if c.ID == needleID && strings.Contains(c.Content, "quantum_flux_capacitor_9991") {
			foundNeedle = true
		}
		if c.Kind == "cold_index" && strings.Contains(c.Content, "[ctx:chk_") {
			foundColdIndex = true
		}
	}
	if !foundNeedle {
		t.Fatal("expected BM25 pager to auto-recall buried needle chunk from 10M cold store")
	}
	if !foundColdIndex {
		t.Fatal("expected compact cold-index handle chunk in projected window")
	}

	// Append another turn and verify Tier 1 cache-stable prefix is 100% byte-identical!
	chunksNext := append(append([]Chunk(nil), chunks...), Chunk{
		ID:      "chk_hot0000000000099",
		Role:    "assistant",
		Content: "Follow-up answer after turn N+1",
		Tokens:  60,
	})
	proj2 := ProjectWindow(chunksNext, 8_000, "quantum_flux_capacitor_9991 allocator ring buffer")
	if proj2.PinnedCount != proj1.PinnedCount {
		t.Fatalf("PinnedCount changed across turns: %d -> %d", proj1.PinnedCount, proj2.PinnedCount)
	}
	for i := 0; i < proj1.PinnedCount; i++ {
		if proj1.Chunks[i].ID != proj2.Chunks[i].ID ||
			proj1.Chunks[i].Role != proj2.Chunks[i].Role ||
			proj1.Chunks[i].Content != proj2.Chunks[i].Content {
			t.Fatalf("prefix chunk %d drifted across turns: %+v vs %+v", i, proj1.Chunks[i], proj2.Chunks[i])
		}
	}
}

func TestProjectWindow_NeverSplitsToolCallResultPairs(t *testing.T) {
	chunks := []Chunk{
		{ID: "chk_sys0000000000001", Role: "system", Content: "sys", Tokens: 20},
		// Old user turn
		{ID: "chk_old0000000000002", Role: "user", Content: strings.Repeat("old ", 200), Tokens: 500},
		// A large tool-call + 2 tool-results group that will NOT fit in a tight hot tail alongside the final turn.
		{
			ID: "chk_asst_tc00000003", Role: "assistant", Content: "running tools",
			ToolCalls: []ThreadToolCall{
				{ID: "call_alpha", Type: "function", Name: "read_file", Arguments: `{"path":"a.go"}`},
				{ID: "call_beta", Type: "function", Name: "read_file", Arguments: `{"path":"b.go"}`},
			},
			Tokens: 150,
		},
		{ID: "chk_tool_a000000004", Role: "tool", ToolCallID: "call_alpha", Content: strings.Repeat("alpha ", 120), Tokens: 300},
		{ID: "chk_tool_b000000005", Role: "tool", ToolCallID: "call_beta", Content: strings.Repeat("beta ", 120), Tokens: 300},
		// Latest tool-call pair that DOES fit in the hot tail.
		{
			ID: "chk_asst_tc00000006", Role: "assistant", Content: "checking status",
			ToolCalls: []ThreadToolCall{
				{ID: "call_gamma", Type: "function", Name: "git_status", Arguments: `{}`},
			},
			Tokens: 40,
		},
		{ID: "chk_tool_g000000007", Role: "tool", ToolCallID: "call_gamma", Content: "clean", Tokens: 30},
		{ID: "chk_user_end0000008", Role: "user", Content: "great, proceed", Tokens: 20},
	}

	// Target 350 tokens: enough for sys (20) + cold handles + latest tool pair (70) + final user (20),
	// NOT enough for the earlier 750-token (call_alpha + call_beta) group.
	proj := ProjectWindow(chunks, 350, "")

	// Invariant: EVERY tool message present in proj.Chunks MUST have its matching
	// assistant tool_call present BEFORE it, and EVERY assistant tool_call MUST have
	// all of its tool results present!
	declared := map[string]bool{}
	answered := map[string]bool{}
	for _, c := range proj.Chunks {
		if c.Role == "assistant" {
			for _, tc := range c.ToolCalls {
				declared[tc.ID] = true
			}
		}
		if c.Role == "tool" {
			if !declared[c.ToolCallID] {
				t.Fatalf("ORPHANED tool result %q appeared without preceding assistant tool_call in projected window!", c.ToolCallID)
			}
			answered[c.ToolCallID] = true
		}
	}
	for id := range declared {
		if !answered[id] {
			t.Fatalf("SPLIT assistant tool_call %q appeared without its tool result!", id)
		}
	}
	if !declared["call_gamma"] || !answered["call_gamma"] {
		t.Fatalf("expected latest intact tool pair call_gamma in hot tail, got declared=%v answered=%v", declared, answered)
	}
	if declared["call_alpha"] || answered["call_alpha"] || answered["call_beta"] {
		t.Fatalf("oversized older tool group should be cold-paged as a whole, got declared=%v answered=%v", declared, answered)
	}
}

func TestThreadStore_SearchChunksAndLosslessRecall(t *testing.T) {
	store := NewMemoryThreadStore()
	thr, err := store.CreateThread("acme", "alice", "search test", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = store.AppendChunk(thr.ID, "main", nil, Chunk{
		Role: "user", Content: "Configure Kubernetes ingress controller TLS certificates",
	})
	target, _, _ := store.AppendChunk(thr.ID, "main", nil, Chunk{
		Role: "assistant", Content: "Fix deadlock in Raft consensus heartbeat election timer on shard 9",
	})
	_, _, _ = store.AppendChunk(thr.ID, "main", nil, Chunk{
		Role: "user", Content: "Update README badges and formatting",
	})

	hits, err := store.SearchChunks(thr.ID, "main", "Raft heartbeat deadlock", 5)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(hits) == 0 || hits[0].ChunkID != target.ID {
		t.Fatalf("SearchChunks top hit = %+v, want %s", hits, target.ID)
	}

	// Recall exact full bytes via GetChunk.
	sl, err := store.GetChunk(thr.ID, hits[0].ChunkID, 0, 0)
	if err != nil {
		t.Fatalf("GetChunk: %v", err)
	}
	if sl.Content != target.Content {
		t.Fatalf("recalled content = %q, want %q", sl.Content, target.Content)
	}
}
