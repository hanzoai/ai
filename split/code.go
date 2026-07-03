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

import (
	"strings"

	"github.com/hanzoai/ai/model"
)

// CodeSplitProvider chunks SOURCE CODE along structural boundaries — declarations
// (functions, types, classes, impls) — instead of blind line windows, so each chunk
// is a semantically-complete unit: a whole function keeps its signature with its body,
// which is what makes code retrieval actually useful. This is the standard
// language-aware RAG chunking (the RecursiveCharacter/from_language approach): try the
// most-structural separator first and only fall to finer ones when a unit still
// exceeds the token budget. Orthogonal to the Markdown/QA/Basic/Default providers —
// same SplitText contract, selected per file extension by splitTypeForFile.
type CodeSplitProvider struct {
	separators []string
	maxTokens  int
}

// codeSeparators maps a language key (file-extension stem) to its declaration
// boundaries, ordered most-structural → least. The trailing generic separators
// (blank line, newline, space) are the recursive fallback every language shares, so a
// single oversized declaration still gets divided rather than blowing the budget.
var codeSeparators = map[string][]string{
	"go":   {"\nfunc ", "\ntype ", "\nvar ", "\nconst ", "\n\n", "\n", " "},
	"py":   {"\nclass ", "\ndef ", "\n\tdef ", "\n\n", "\n", " "},
	"ts":   {"\nexport ", "\nfunction ", "\nclass ", "\ninterface ", "\ntype ", "\nconst ", "\n\n", "\n", " "},
	"tsx":  {"\nexport ", "\nfunction ", "\nclass ", "\nconst ", "\n\n", "\n", " "},
	"js":   {"\nexport ", "\nfunction ", "\nclass ", "\nconst ", "\n\n", "\n", " "},
	"jsx":  {"\nexport ", "\nfunction ", "\nclass ", "\nconst ", "\n\n", "\n", " "},
	"rs":   {"\npub fn ", "\nfn ", "\nimpl ", "\nstruct ", "\nenum ", "\ntrait ", "\nmod ", "\n\n", "\n", " "},
	"java": {"\npublic ", "\nprivate ", "\nprotected ", "\nclass ", "\ninterface ", "\n\n", "\n", " "},
	"kt":   {"\nfun ", "\nclass ", "\nobject ", "\ninterface ", "\nval ", "\n\n", "\n", " "},
	"rb":   {"\ndef ", "\nclass ", "\nmodule ", "\n\n", "\n", " "},
	"php":  {"\nfunction ", "\nclass ", "\ntrait ", "\n\n", "\n", " "},
	"cpp":  {"\nclass ", "\nstruct ", "\nnamespace ", "\n}\n", "\n\n", "\n", " "},
	"c":    {"\nstruct ", "\n}\n", "\n\n", "\n", " "},
	"cs":   {"\npublic ", "\nprivate ", "\nprotected ", "\nclass ", "\nnamespace ", "\n\n", "\n", " "},
	"swift": {"\nfunc ", "\nclass ", "\nstruct ", "\nenum ", "\nprotocol ", "\n\n", "\n", " "},
	"scala": {"\ndef ", "\nclass ", "\nobject ", "\ntrait ", "\n\n", "\n", " "},
	"sol":  {"\nfunction ", "\ncontract ", "\nmodifier ", "\nevent ", "\nstruct ", "\n\n", "\n", " "},
	"sh":   {"\nfunction ", "\n\n", "\n", " "},
	"proto": {"\nmessage ", "\nservice ", "\nenum ", "\n\n", "\n", " "},
}

// genericCodeSeparators back any language without a specific entry — still far better
// than blind line windows because it prefers blank-line (block) boundaries.
var genericCodeSeparators = []string{"\n\n", "\n", " "}

// NewCodeSplitProvider builds a splitter for a language key (extension stem, e.g. "go").
// Unknown languages fall back to the generic block separators — always structural.
func NewCodeSplitProvider(lang string) (*CodeSplitProvider, error) {
	seps, ok := codeSeparators[lang]
	if !ok {
		seps = genericCodeSeparators
	}
	return &CodeSplitProvider{separators: seps, maxTokens: 400}, nil
}

// SplitText recursively splits code on the most-structural separator that keeps each
// piece within the token budget, then coalesces tiny adjacent pieces so a lone
// signature or one-line type isn't stranded as its own chunk.
func (p *CodeSplitProvider) SplitText(text string) ([]string, error) {
	chunks, err := p.recurse(text, 0)
	if err != nil {
		return nil, err
	}
	return p.coalesce(chunks)
}

// recurse emits text as one chunk when it fits the budget (or separators are
// exhausted); otherwise it splits on separators[i] and re-splits each oversized piece
// on the next-finer separator. Splitting keeps the boundary marker attached to the
// declaration it starts, so "func Foo(" is never severed from its body's first line.
func (p *CodeSplitProvider) recurse(text string, i int) ([]string, error) {
	tok, err := model.GetTokenSize("gpt-3.5-turbo", text)
	if err != nil {
		return nil, err
	}
	if tok <= p.maxTokens || i >= len(p.separators) {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []string{text}, nil
	}
	var out []string
	for _, part := range splitKeepingBoundary(text, p.separators[i]) {
		sub, err := p.recurse(part, i+1)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// coalesce merges adjacent chunks while their combined size stays within budget, so
// declaration chunks pack densely instead of one-tiny-chunk-per-type.
func (p *CodeSplitProvider) coalesce(chunks []string) ([]string, error) {
	var out []string
	var cur string
	for _, c := range chunks {
		cand := c
		if cur != "" {
			cand = cur + "\n" + c
		}
		tok, err := model.GetTokenSize("gpt-3.5-turbo", cand)
		if err != nil {
			return nil, err
		}
		if cur == "" || tok <= p.maxTokens {
			cur = cand
			continue
		}
		out = append(out, cur)
		cur = c
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out, nil
}

// splitKeepingBoundary splits on sep, re-attaching a declaration separator (one that
// carries a keyword, e.g. "\nfunc ") to the START of each following piece so the
// boundary token stays with its declaration. Pure-whitespace separators ("\n\n",
// "\n", " ") are the last-resort fallback and split plainly.
func splitKeepingBoundary(text, sep string) []string {
	if strings.TrimSpace(sep) == "" {
		return strings.Split(text, sep)
	}
	pieces := strings.Split(text, sep)
	out := make([]string, 0, len(pieces))
	for i, pc := range pieces {
		if i == 0 {
			if strings.TrimSpace(pc) != "" {
				out = append(out, pc)
			}
			continue
		}
		out = append(out, sep[1:]+pc) // drop the leading '\n', keep the keyword marker
	}
	return out
}
