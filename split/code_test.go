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
	"testing"
)

// TestCodeSplitKeepsDeclarationsWhole verifies the structural splitter cuts BETWEEN
// declarations, not through them: a file of several budget-sized funcs is too big for
// one chunk, so it must split — and every chunk begins at a declaration boundary
// ("func " or the file head), never mid-body. (A single function larger than the
// budget must of course split internally — that's a separate, unavoidable case.)
func TestCodeSplitKeepsDeclarationsWhole(t *testing.T) {
	p, err := NewCodeSplitProvider("go")
	if err != nil {
		t.Fatal(err)
	}
	// Ten funcs, each comfortably under the token budget; the whole file exceeds it,
	// so it must split across func boundaries.
	names := []string{"Alpha", "Beta", "Gamma", "Delta", "Epsilon", "Zeta", "Eta", "Theta", "Iota", "Kappa"}
	body := strings.Repeat("\n\tresult := callSomeHelperFunction(argument, other)", 16) + "\n\treturn\n"
	src := "package x\n"
	for _, n := range names {
		src += "\nfunc " + n + "() {" + body + "}\n"
	}
	chunks, err := p.SplitText(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected the oversized file to split across func boundaries, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		first := strings.TrimSpace(c)
		if head := first[:min(24, len(first))]; !strings.HasPrefix(first, "func ") && !strings.HasPrefix(first, "package ") {
			t.Errorf("chunk[%d] does not begin at a declaration boundary: %q", i, head)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestCodeSplitUnknownLanguageFallsBack proves an unmapped language does not crash and
// still splits on block boundaries (generic separators), never returning zero chunks
// for non-empty input.
func TestCodeSplitUnknownLanguageFallsBack(t *testing.T) {
	p, err := NewCodeSplitProvider("cobol-from-mars")
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := p.SplitText("PROC A.\n\nPROC B.\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("unknown language must still yield chunks for non-empty input")
	}
}

// TestSplitKeepingBoundaryReattachesKeyword proves the declaration keyword stays with
// the piece it opens (so "func Foo" is never split from "Foo").
func TestSplitKeepingBoundaryReattachesKeyword(t *testing.T) {
	parts := splitKeepingBoundary("head\nfunc A(){}\nfunc B(){}", "\nfunc ")
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d: %#v", len(parts), parts)
	}
	if parts[1] != "func A(){}" || parts[2] != "func B(){}" {
		t.Errorf("boundary keyword not reattached: %#v", parts)
	}
}
