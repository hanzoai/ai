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

package split

import "strings"

// RecursiveSplitProvider is a character-based recursive text splitter with the
// same contract as LangChain's RecursiveCharacterTextSplitter: it splits on a
// cascade of separators (paragraph → line → word → char), keeping each chunk at
// or below ChunkSize characters and overlapping consecutive chunks by
// ChunkOverlap characters.
//
// It exists so the Hanzo RAG surface (/v1/rag/*, controllers/rag.go →
// object.Rag*) chunks documents identically to the retired standalone rag-api
// (danny-avila fork), which configured RecursiveCharacterTextSplitter with
// CHUNK_SIZE=1500 / CHUNK_OVERLAP=100. Chunking is a splitting concern, so it
// lives here in the ONE splitting home rather than being reinvented in the RAG
// module — GetSplitProvider("Recursive") returns it.
type RecursiveSplitProvider struct {
	ChunkSize    int
	ChunkOverlap int
	Separators   []string
}

// defaultRecursiveSeparators mirrors LangChain's default cascade: split on
// blank lines first, then single newlines, then spaces, then (last resort)
// individual characters ("" means split into runes).
var defaultRecursiveSeparators = []string{"\n\n", "\n", " ", ""}

// NewRecursiveSplitProvider builds a recursive splitter. Non-positive chunkSize
// falls back to 1500 and a negative overlap to 100 — the retired rag-api
// defaults — so a zero-value construction is still the parity configuration.
// Overlap is clamped strictly below chunkSize (an overlap ≥ size cannot make
// progress and would loop).
func NewRecursiveSplitProvider(chunkSize, chunkOverlap int) (*RecursiveSplitProvider, error) {
	if chunkSize <= 0 {
		chunkSize = 1500
	}
	if chunkOverlap < 0 {
		chunkOverlap = 100
	}
	if chunkOverlap >= chunkSize {
		chunkOverlap = chunkSize / 4
	}
	return &RecursiveSplitProvider{
		ChunkSize:    chunkSize,
		ChunkOverlap: chunkOverlap,
		Separators:   defaultRecursiveSeparators,
	}, nil
}

func (p *RecursiveSplitProvider) SplitText(text string) ([]string, error) {
	return p.splitRecursive(text, p.Separators), nil
}

// splitRecursive picks the first separator that occurs in text (falling back to
// the last, "", = per-character), splits on it, and recurses into any fragment
// still larger than ChunkSize using the remaining, finer separators. Fragments
// that fit are merged back up to ChunkSize with ChunkOverlap.
func (p *RecursiveSplitProvider) splitRecursive(text string, separators []string) []string {
	if text == "" {
		return nil
	}

	separator := separators[len(separators)-1]
	remaining := []string{}
	for i, sep := range separators {
		if sep == "" {
			separator = sep
			break
		}
		if strings.Contains(text, sep) {
			separator = sep
			remaining = separators[i+1:]
			break
		}
	}

	splits := splitBySeparator(text, separator)

	final := []string{}
	good := []string{}
	for _, s := range splits {
		if len([]rune(s)) <= p.ChunkSize {
			good = append(good, s)
			continue
		}
		// Flush accumulated good splits, then recurse into the oversized one.
		if len(good) > 0 {
			final = append(final, p.mergeSplits(good, separator)...)
			good = good[:0]
		}
		if len(remaining) == 0 {
			final = append(final, s)
		} else {
			final = append(final, p.splitRecursive(s, remaining)...)
		}
	}
	if len(good) > 0 {
		final = append(final, p.mergeSplits(good, separator)...)
	}
	return final
}

// splitBySeparator splits on a non-empty separator (dropping empty fragments),
// or into individual runes when the separator is "".
func splitBySeparator(text, separator string) []string {
	if separator == "" {
		runes := []rune(text)
		out := make([]string, len(runes))
		for i, r := range runes {
			out[i] = string(r)
		}
		return out
	}
	parts := strings.Split(text, separator)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// mergeSplits greedily packs fragments (rejoined by separator) into chunks no
// larger than ChunkSize, then slides a ChunkOverlap-sized tail into the next
// chunk. This mirrors LangChain's _merge_splits so retrieval recall matches the
// retired rag-api. Length is measured in runes (Unicode-safe, matching Python
// len() over str).
func (p *RecursiveSplitProvider) mergeSplits(splits []string, separator string) []string {
	sepLen := len([]rune(separator))
	docs := []string{}
	current := []string{}
	total := 0

	for _, s := range splits {
		sLen := len([]rune(s))
		joinLen := sLen
		if len(current) > 0 {
			joinLen += sepLen
		}
		if total+joinLen > p.ChunkSize && len(current) > 0 {
			docs = append(docs, joinNonEmpty(current, separator))
			// Slide the window: drop from the front until the running total is
			// within ChunkOverlap (and would fit the incoming fragment).
			for total > p.ChunkOverlap || (total+joinLen > p.ChunkSize && total > 0) {
				drop := len([]rune(current[0]))
				if len(current) > 1 {
					drop += sepLen
				}
				total -= drop
				current = current[1:]
				if len(current) == 0 {
					break
				}
			}
		}
		if len(current) > 0 {
			total += sepLen
		}
		current = append(current, s)
		total += sLen
	}
	if len(current) > 0 {
		docs = append(docs, joinNonEmpty(current, separator))
	}
	return docs
}

func joinNonEmpty(parts []string, separator string) string {
	joined := strings.Join(parts, separator)
	return strings.TrimSpace(joined)
}
