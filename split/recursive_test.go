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

package split_test

import (
	"strings"
	"testing"

	"github.com/hanzoai/ai/split"
)

// A zero-value construction must yield the retired rag-api parity defaults
// (RecursiveCharacterTextSplitter CHUNK_SIZE=1500 / CHUNK_OVERLAP=100) so the
// RAG surface chunks documents identically to the standalone service it replaces.
func TestRecursiveSplitProvider_ParityDefaults(t *testing.T) {
	p, err := split.NewRecursiveSplitProvider(0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if p.ChunkSize != 1500 || p.ChunkOverlap != 100 {
		t.Fatalf("defaults = %d/%d, want 1500/100", p.ChunkSize, p.ChunkOverlap)
	}
}

// An overlap >= size cannot make forward progress; the constructor must clamp it.
func TestRecursiveSplitProvider_ClampsOverlap(t *testing.T) {
	p, err := split.NewRecursiveSplitProvider(100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if p.ChunkOverlap >= p.ChunkSize {
		t.Fatalf("overlap %d must be clamped below size %d", p.ChunkOverlap, p.ChunkSize)
	}
}

// Text that fits within ChunkSize is returned as a single (trimmed) chunk.
func TestRecursiveSplitProvider_ShortTextSingleChunk(t *testing.T) {
	p, _ := split.NewRecursiveSplitProvider(1500, 100)
	got, err := p.SplitText("a short paragraph that comfortably fits")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 chunk, got %d: %q", len(got), got)
	}
}

// Every emitted chunk must be at or below ChunkSize (measured in runes, matching
// Python len() over str), and long input must split into multiple chunks.
func TestRecursiveSplitProvider_RespectsChunkSize(t *testing.T) {
	p, _ := split.NewRecursiveSplitProvider(50, 10)
	text := strings.Repeat("word ", 200) // 1000 chars of space-separated tokens
	got, err := p.SplitText(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("expected the long input to split into multiple chunks, got %d", len(got))
	}
	for i, c := range got {
		if n := len([]rune(c)); n > 50 {
			t.Fatalf("chunk %d has %d runes, exceeds ChunkSize 50: %q", i, n, c)
		}
	}
}

// Overlap must duplicate boundary content: splitting the same text with a
// positive overlap yields strictly more total characters than with no overlap.
func TestRecursiveSplitProvider_OverlapDuplicatesContent(t *testing.T) {
	text := strings.Repeat("lorem ipsum dolor ", 100)
	overlapped, _ := split.NewRecursiveSplitProvider(80, 20)
	plain, _ := split.NewRecursiveSplitProvider(80, 0)

	ov, err := overlapped.SplitText(text)
	if err != nil {
		t.Fatal(err)
	}
	no, err := plain.SplitText(text)
	if err != nil {
		t.Fatal(err)
	}

	sum := func(ss []string) int {
		n := 0
		for _, s := range ss {
			n += len([]rune(s))
		}
		return n
	}
	if sum(ov) <= sum(no) {
		t.Fatalf("overlap should duplicate boundary content: overlap total %d must exceed no-overlap total %d", sum(ov), sum(no))
	}
}

// The factory wires "Recursive" to the parity-configured provider.
func TestGetSplitProvider_Recursive(t *testing.T) {
	p, err := split.GetSplitProvider("Recursive")
	if err != nil {
		t.Fatal(err)
	}
	rp, ok := p.(*split.RecursiveSplitProvider)
	if !ok {
		t.Fatalf("GetSplitProvider(\"Recursive\") = %T, want *split.RecursiveSplitProvider", p)
	}
	if rp.ChunkSize != 1500 || rp.ChunkOverlap != 100 {
		t.Fatalf("factory provider = %d/%d, want parity 1500/100", rp.ChunkSize, rp.ChunkOverlap)
	}
}
