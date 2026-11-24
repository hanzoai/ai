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
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	defaultProjectTargetTokens = 128_000
	defaultSearchLimit         = 10
	handleSummaryMaxChars      = 84
)

// ColdChunkHandle is a compact, deterministic reference to a cold chunk that
// was not paged in verbatim, ensuring nothing in a 10M+ thread is ever lost.
type ColdChunkHandle struct {
	ChunkID string `json:"chunk_id"`
	Role    string `json:"role"`
	Tokens  int    `json:"tokens"`
	Summary string `json:"summary"`
	Handle  string `json:"handle"`
}

// ProjectedWindow is the 3-tier projected view of a 10M+ thread tailored to a
// model's serving context budget while keeping the prompt prefix cache-stable
// and tool call/result pairs intact.
type ProjectedWindow struct {
	Chunks            []Chunk           `json:"chunks"`
	PinnedCount       int               `json:"pinned_count"`
	RecalledCount     int               `json:"recalled_count"`
	ColdHandleCount   int               `json:"cold_handle_count"`
	HotTailCount      int               `json:"hot_tail_count"`
	TotalSourceTokens int               `json:"total_source_tokens"`
	ProjectedTokens   int               `json:"projected_tokens"`
	ColdHandles       []ColdChunkHandle `json:"cold_handles,omitempty"`
}

// ChunkSearchResult is one BM25 hit returned by ThreadStore.SearchChunks.
type ChunkSearchResult struct {
	ChunkID   string    `json:"chunk_id"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind,omitempty"`
	Tokens    int       `json:"tokens"`
	Score     float64   `json:"score"`
	Snippet   string    `json:"snippet"`
	CreatedAt time.Time `json:"created_at"`
}

// FormatColdHandle renders the canonical deterministic compact handle for a cold chunk:
// `[ctx:chk_<id> | <tokens> tok | <summary>]`.
func FormatColdHandle(c Chunk) ColdChunkHandle {
	toks := ChunkTokens(c)
	summary := summarizeChunkForHandle(c)
	id := c.ID
	if id == "" {
		id = computeChunkID(c)
	}
	handle := fmt.Sprintf("[ctx:%s | %d tok | %s]", id, toks, summary)
	return ColdChunkHandle{
		ChunkID: id,
		Role:    c.Role,
		Tokens:  toks,
		Summary: summary,
		Handle:  handle,
	}
}

func summarizeChunkForHandle(c Chunk) string {
	var base string
	switch {
	case c.Handoff != nil:
		base = fmt.Sprintf("handoff->%s: %s", c.Handoff.ToActor, c.Content)
	case len(c.ToolCalls) > 0:
		names := make([]string, 0, len(c.ToolCalls))
		for _, tc := range c.ToolCalls {
			if tc.Name != "" {
				names = append(names, tc.Name)
			}
		}
		if c.Content != "" {
			base = fmt.Sprintf("tools(%s) %s", strings.Join(names, ","), c.Content)
		} else {
			base = fmt.Sprintf("tools(%s)", strings.Join(names, ","))
		}
	case c.ToolCallID != "":
		base = fmt.Sprintf("tool[%s]: %s", c.ToolCallID, c.Content)
	default:
		base = c.Content
	}
	base = collapseWhitespace(base)
	if len(base) > handleSummaryMaxChars {
		base = base[:handleSummaryMaxChars-3] + "..."
	}
	if base == "" {
		base = "(empty)"
	}
	role := c.Role
	if role == "" {
		role = "message"
	}
	return role + ": " + base
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// turnGroup is an indivisible unit of one or more chunks: either a single message
// or an assistant message with tool_calls paired with all of its tool results.
type turnGroup struct {
	chunks []Chunk
	tokens int
	order  int
}

// groupAtomicTurns partitions chunks into indivisible turn groups so that an
// (assistant tool_calls, tool results...) exchange is NEVER split across cold/hot
// boundaries. Orphaned leading tool chunks without an assistant tool_call are
// skipped from standalone promotion into the hot tail if detached.
func groupAtomicTurns(chunks []Chunk) []turnGroup {
	groups := make([]turnGroup, 0, len(chunks))
	used := make([]bool, len(chunks))

	for i := 0; i < len(chunks); i++ {
		if used[i] {
			continue
		}
		c := chunks[i]
		if c.Role == "assistant" && len(c.ToolCalls) > 0 {
			wantIDs := make(map[string]bool, len(c.ToolCalls))
			for _, tc := range c.ToolCalls {
				if tc.ID != "" {
					wantIDs[tc.ID] = true
				}
			}
			grp := turnGroup{
				chunks: []Chunk{c},
				tokens: ChunkTokens(c),
				order:  i,
			}
			used[i] = true
			// Attach all subsequent tool result chunks that answer these tool calls.
			for j := i + 1; j < len(chunks); j++ {
				if used[j] {
					continue
				}
				next := chunks[j]
				if next.Role != "tool" {
					// If we already matched all explicit IDs or encountered another
					// non-tool turn, stop grouping.
					if len(wantIDs) == 0 {
						break
					}
					continue
				}
				if len(wantIDs) == 0 || wantIDs[next.ToolCallID] {
					grp.chunks = append(grp.chunks, next)
					grp.tokens += ChunkTokens(next)
					used[j] = true
					delete(wantIDs, next.ToolCallID)
				}
			}
			groups = append(groups, grp)
			continue
		}
		used[i] = true
		groups = append(groups, turnGroup{
			chunks: []Chunk{c},
			tokens: ChunkTokens(c),
			order:  i,
		})
	}
	return groups
}

// ProjectWindow implements the 3-tier lossless 10M+ context pager:
//
//  1. Cache-stable pinned prefix: leading system/developer turns + explicitly
//     Pinned chunks are preserved byte-identically at the front of the window
//     for >90% prompt-cache hit rates.
//  2. Indexed 10M+ cold store: older turns beyond the hot-tail budget are scored
//     via BM25 against query; top matching groups are auto-paged in verbatim
//     while all other cold chunks are represented by compact deterministic
//     handles (`[ctx:chk_<id> | <tokens> tok | <summary>]`).
//  3. Verbatim hot tail: recent turns + intact (assistant tool_calls, tool result)
//     pairs are never split, preventing orphaned tool_call_id 400 errors.
func ProjectWindow(chunks []Chunk, targetTokens int, query string) ProjectedWindow {
	if targetTokens <= 0 {
		targetTokens = defaultProjectTargetTokens
	}

	totalSource := 0
	for i := range chunks {
		chunks[i].Tokens = ChunkTokens(chunks[i])
		totalSource += chunks[i].Tokens
	}

	// Fast path: everything fits verbatim within the target token budget.
	if totalSource <= targetTokens {
		pinnedCount := countLeadingPrefix(chunks)
		out := append([]Chunk(nil), chunks...)
		return ProjectedWindow{
			Chunks:            out,
			PinnedCount:       pinnedCount,
			HotTailCount:      len(out) - pinnedCount,
			TotalSourceTokens: totalSource,
			ProjectedTokens:   totalSource,
		}
	}

	// Tier 1: Extract cache-stable pinned prefix (leading system/developer turns +
	// explicitly Pinned chunks), preserving exact byte-identical order.
	isPrefix := make([]bool, len(chunks))
	leading := countLeadingPrefix(chunks)
	for i := 0; i < leading; i++ {
		isPrefix[i] = true
	}
	for i := leading; i < len(chunks); i++ {
		if chunks[i].Pinned {
			isPrefix[i] = true
		}
	}

	var prefix []Chunk
	var rest []Chunk
	prefixTokens := 0
	for i, c := range chunks {
		if isPrefix[i] {
			prefix = append(prefix, c)
			prefixTokens += c.Tokens
		} else {
			rest = append(rest, c)
		}
	}

	groups := groupAtomicTurns(rest)
	remaining := targetTokens - prefixTokens
	if remaining < 0 {
		remaining = 0
	}

	// Budget split between hot tail (Tier 3) and cold recall/handles (Tier 2).
	hotBudget := int(float64(remaining) * 0.65)
	if strings.TrimSpace(query) == "" {
		hotBudget = int(float64(remaining) * 0.80)
	}
	if hotBudget < 0 {
		hotBudget = 0
	}

	// Tier 3: Select verbatim hot tail from newest atomic groups backwards.
	hotFlags := make([]bool, len(groups))
	hotTokens := 0
	hotStartIdx := len(groups)
	for i := len(groups) - 1; i >= 0; i-- {
		g := groups[i]
		// Never include an orphaned standalone tool message without its assistant call.
		if len(g.chunks) == 1 && g.chunks[0].Role == "tool" {
			continue
		}
		if hotTokens+g.tokens <= hotBudget || (hotStartIdx == len(groups) && g.tokens <= remaining) {
			hotFlags[i] = true
			hotTokens += g.tokens
			hotStartIdx = i
		} else {
			// Stop extending the contiguous hot tail once we hit a group that does not fit
			// so recency order remains clean and contiguous.
			break
		}
	}

	// Identify cold candidate groups (everything not selected into hot tail).
	var coldIndices []int
	for i := range groups {
		if !hotFlags[i] {
			coldIndices = append(coldIndices, i)
		}
	}

	// Tier 2: Score cold candidate groups with BM25 against query and page in top matches.
	coldBudget := remaining - hotTokens
	if coldBudget < 0 {
		coldBudget = 0
	}

	// Reserve a slice of coldBudget for compact cold handles so cold handles
	// always fit alongside recalled chunks.
	handleReserve := min(coldBudget/3, 4096)
	if len(coldIndices) == 0 {
		handleReserve = 0
	}
	recallBudget := coldBudget - handleReserve
	if recallBudget < 0 {
		recallBudget = 0
	}

	recalledFlags := make([]bool, len(groups))
	recalledTokens := 0
	if strings.TrimSpace(query) != "" && len(coldIndices) > 0 && recallBudget > 0 {
		docs := make([]string, len(coldIndices))
		for k, gi := range coldIndices {
			docs[k] = groupSearchText(groups[gi])
		}
		scores := bm25ScoreDocs(docs, query)
		type scoredIdx struct {
			groupIdx int
			score    float64
		}
		ranked := make([]scoredIdx, 0, len(coldIndices))
		for k, sc := range scores {
			if sc > 0 {
				gi := coldIndices[k]
				// Never recall an orphaned standalone tool message.
				if len(groups[gi].chunks) == 1 && groups[gi].chunks[0].Role == "tool" {
					continue
				}
				ranked = append(ranked, scoredIdx{groupIdx: gi, score: sc})
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool {
			if ranked[i].score != ranked[j].score {
				return ranked[i].score > ranked[j].score
			}
			return ranked[i].groupIdx > ranked[j].groupIdx // prefer newer on tie
		})
		for _, item := range ranked {
			g := groups[item.groupIdx]
			if recalledTokens+g.tokens <= recallBudget {
				recalledFlags[item.groupIdx] = true
				recalledTokens += g.tokens
			}
		}
	}

	// Build compact deterministic handles for every cold chunk NOT recalled verbatim.
	var coldHandles []ColdChunkHandle
	for _, gi := range coldIndices {
		if recalledFlags[gi] {
			continue
		}
		for _, c := range groups[gi].chunks {
			coldHandles = append(coldHandles, FormatColdHandle(c))
		}
	}

	// Build the bounded cold-index chunk that fits inside the remaining handle budget.
	handleBudget := remaining - hotTokens - recalledTokens
	if handleBudget < 0 {
		handleBudget = 0
	}
	var coldIndexChunk *Chunk
	if len(coldHandles) > 0 && handleBudget >= 24 {
		coldIndexChunk = buildColdIndexChunk(coldHandles, handleBudget)
	}

	// Assemble final projected chunks in cache-stable chronological order:
	//   [1] Cache-stable pinned prefix (byte-identical across turns)
	//   [2] Compact cold-index handles chunk (if any cold chunks exist)
	//   [3] Auto-recalled cold groups (in original chronological order, tool pairs intact)
	//   [4] Verbatim hot tail groups (in original chronological order, tool pairs intact)
	out := make([]Chunk, 0, len(prefix)+1+len(rest))
	out = append(out, prefix...)
	projectedTokens := prefixTokens

	if coldIndexChunk != nil {
		out = append(out, *coldIndexChunk)
		projectedTokens += coldIndexChunk.Tokens
	}

	recalledCount := 0
	for i, g := range groups {
		if recalledFlags[i] {
			out = append(out, g.chunks...)
			recalledCount += len(g.chunks)
			projectedTokens += g.tokens
		}
	}

	hotCount := 0
	for i, g := range groups {
		if hotFlags[i] {
			out = append(out, g.chunks...)
			hotCount += len(g.chunks)
			projectedTokens += g.tokens
		}
	}

	return ProjectedWindow{
		Chunks:            out,
		PinnedCount:       len(prefix),
		RecalledCount:     recalledCount,
		ColdHandleCount:   len(coldHandles),
		HotTailCount:      hotCount,
		TotalSourceTokens: totalSource,
		ProjectedTokens:   projectedTokens,
		ColdHandles:       coldHandles,
	}
}

func countLeadingPrefix(chunks []Chunk) int {
	n := 0
	for _, c := range chunks {
		role := strings.ToLower(strings.TrimSpace(c.Role))
		if role == "system" || role == "developer" {
			n++
			continue
		}
		break
	}
	return n
}

func buildColdIndexChunk(handles []ColdChunkHandle, budgetTokens int) *Chunk {
	header := fmt.Sprintf("Archived thread context (%d cold chunk(s); recall via GET /v1/threads/:id/chunks/:chunkId or POST /v1/threads/:id/search):\n", len(handles))
	var b strings.Builder
	b.WriteString(header)
	usedTokens := EstimateTokens(header) + 6

	omitted := 0
	omittedTokens := 0
	for i, h := range handles {
		line := h.Handle + "\n"
		lineTok := EstimateTokens(line)
		// Leave room for an overflow summary line if more handles remain.
		reserve := 0
		if i < len(handles)-1 {
			reserve = 28
		}
		if usedTokens+lineTok+reserve > budgetTokens {
			omitted = len(handles) - i
			for j := i; j < len(handles); j++ {
				omittedTokens += handles[j].Tokens
			}
			break
		}
		b.WriteString(line)
		usedTokens += lineTok
	}
	if omitted > 0 {
		first := handles[len(handles)-omitted].ChunkID
		last := handles[len(handles)-1].ChunkID
		fmt.Fprintf(&b, "[ctx:%s..%s | +%d cold chunks (%d tok) omitted from inline index; use search/get_chunk for lossless recall]",
			first, last, omitted, omittedTokens)
	}
	content := strings.TrimRight(b.String(), "\n")
	c := Chunk{
		Kind:    "cold_index",
		Role:    "system",
		Content: content,
		Tokens:  EstimateTokens(content) + 4,
	}
	c.ID = computeChunkID(c)
	return &c
}

func groupSearchText(g turnGroup) string {
	var b strings.Builder
	for _, c := range g.chunks {
		b.WriteString(c.Content)
		b.WriteByte(' ')
		for _, tc := range c.ToolCalls {
			b.WriteString(tc.Name)
			b.WriteByte(' ')
			b.WriteString(tc.Arguments)
			b.WriteByte(' ')
		}
		if c.Handoff != nil {
			b.WriteString(c.Handoff.Note)
			b.WriteByte(' ')
			b.WriteString(c.Handoff.GitRef)
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// SearchChunks performs BM25 relevance search across all chunks in (threadID, branch)
// and returns the top limit matching chunks for lossless on-demand recall.
func (s *ThreadStore) SearchChunks(threadID, branch, query string, limit int) ([]ChunkSearchResult, error) {
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	chunks, err := s.GetBranchChunks(threadID, branch)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 || strings.TrimSpace(query) == "" {
		return []ChunkSearchResult{}, nil
	}

	docs := make([]string, len(chunks))
	for i, c := range chunks {
		docs[i] = groupSearchText(turnGroup{chunks: []Chunk{c}})
	}
	scores := bm25ScoreDocs(docs, query)
	queryTerms := tokenize(query)

	results := make([]ChunkSearchResult, 0, min(limit, len(chunks)))
	for i, sc := range scores {
		if sc <= 0 {
			continue
		}
		c := chunks[i]
		results = append(results, ChunkSearchResult{
			ChunkID:   c.ID,
			Role:      c.Role,
			Kind:      c.Kind,
			Tokens:    ChunkTokens(c),
			Score:     math.Round(sc*10000) / 10000,
			Snippet:   extractSnippet(c.Content, queryTerms, 240),
			CreatedAt: c.CreatedAt,
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// bm25ScoreDocs computes Okapi BM25 relevance scores (k1=1.5, b=0.75) for docs against query.
func bm25ScoreDocs(docs []string, query string) []float64 {
	n := len(docs)
	scores := make([]float64, n)
	qTerms := uniqueTerms(tokenize(query))
	if n == 0 || len(qTerms) == 0 {
		return scores
	}

	const (
		k1 = 1.5
		b  = 0.75
	)

	docTFs := make([]map[string]int, n)
	docLens := make([]int, n)
	df := make(map[string]int, len(qTerms))
	totalLen := 0

	qSet := make(map[string]bool, len(qTerms))
	for _, qt := range qTerms {
		qSet[qt] = true
	}

	for i, d := range docs {
		toks := tokenize(d)
		docLens[i] = len(toks)
		totalLen += len(toks)
		tf := make(map[string]int)
		seen := make(map[string]bool)
		for _, tok := range toks {
			if qSet[tok] {
				tf[tok]++
				if !seen[tok] {
					df[tok]++
					seen[tok] = true
				}
			}
		}
		docTFs[i] = tf
	}

	avgdl := float64(totalLen) / float64(n)
	if avgdl == 0 {
		avgdl = 1
	}

	for i := 0; i < n; i++ {
		tf := docTFs[i]
		if len(tf) == 0 {
			continue
		}
		dl := float64(docLens[i])
		var score float64
		for _, term := range qTerms {
			f := float64(tf[term])
			if f == 0 {
				continue
			}
			dFreq := float64(df[term])
			idf := math.Log((float64(n)-dFreq+0.5)/(dFreq+0.5) + 1.0)
			denom := f + k1*(1.0-b+b*(dl/avgdl))
			score += idf * (f * (k1 + 1.0)) / denom
		}
		scores[i] = score
	}
	return scores
}

func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tok := cur.String()
			if len(tok) >= 2 {
				out = append(out, tok)
			}
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func uniqueTerms(terms []string) []string {
	seen := make(map[string]bool, len(terms))
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func extractSnippet(content string, terms []string, maxLen int) string {
	if content == "" {
		return ""
	}
	lower := strings.ToLower(content)
	bestPos := -1
	for _, t := range terms {
		if idx := strings.Index(lower, t); idx >= 0 {
			if bestPos < 0 || idx < bestPos {
				bestPos = idx
			}
		}
	}
	if bestPos < 0 {
		bestPos = 0
	}
	start := bestPos - maxLen/4
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(content) {
		end = len(content)
	}
	snippet := collapseWhitespace(content[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(content) {
		snippet = snippet + "..."
	}
	return snippet
}
