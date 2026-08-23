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
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// An index is named "{owner}-{store}-docs". The owner is bound to the
// authenticated principal, so the store is the half a caller supplies — and the
// separator is the whole problem: a name that lands on the boundary reads another
// tenant's documents. The two tests below are the two directions it can be
// crossed from, and there is no third.

// DIRECTION ONE — a caller spelling a longer owner than its own.
//
// Org "acme" asking for "corp-docs-hanzo-ai" builds the exact string org
// "acme-corp" builds for its own default.
func TestAStoreCannotSpellALongerOwner(t *testing.T) {
	const def = "docs-hanzo-ai"

	victim := GetSearchIndexName("acme-corp", def)
	if _, err := ResolveStore("acme", "corp-docs-hanzo-ai", def); err == nil {
		t.Fatalf("the crossing store was admitted; it builds %q, which is %q",
			GetSearchIndexName("acme", "corp-docs-hanzo-ai"), victim)
	}
	if GetSearchIndexName("acme", "corp-docs-hanzo-ai") != victim {
		t.Fatal("the collision this test guards has changed shape; re-derive it before trusting the guard")
	}
}

// DIRECTION TWO — the mirror, which the DEFAULT opens.
//
// A default may carry hyphens, so an org can be named to end exactly where a
// victim's default begins. Org "acme-docs-hanzo" asking for the perfectly ordinary
// store "ai" spells the index org "acme" is served by default. The store is
// blameless here; the owner is the half that crosses, so the owner is what is
// refused.
func TestAHyphenatedOwnerCannotChooseAStore(t *testing.T) {
	victim := GetSearchIndexName("acme", "docs-hanzo-ai")
	if GetSearchIndexName("acme-docs-hanzo", "ai") != victim {
		t.Fatal("the mirror collision has changed shape; re-derive it before trusting the guard")
	}
	if _, err := ResolveStore("acme-docs-hanzo", "ai", "docs-hanzo-ai"); err == nil {
		t.Fatalf("a hyphenated owner selected a store and reached %q", victim)
	}

	// The same for the other hyphenated default this estate issues.
	if GetSearchIndexName("acme-rag", "files") != GetSearchIndexName("acme", RagFileStore) {
		t.Fatal("the rag-files mirror has changed shape; re-derive it")
	}
	if _, err := ResolveStore("acme-rag", "files", RagFileStore); err == nil {
		t.Fatal("a hyphenated owner selected a store and reached another org's rag index")
	}
}

// A HYPHENATED ORG IS NOT LOCKED OUT. It keeps its own default store — which is
// where its documents already are — and only loses the ability to name a
// different one, until the hyphenated defaults are renamed and reindexed.
func TestAHyphenatedOwnerStillGetsItsDefault(t *testing.T) {
	for _, def := range []string{"docs-hanzo-ai", DefaultDocsStore, RagFileStore} {
		got, err := ResolveStore("acme-docs-hanzo", "", def)
		if err != nil || got != def {
			t.Errorf("hyphenated owner, no store, default %q = %q, %v; want the default", def, got, err)
		}
		// Naming its own default is asking for the same index, so it is not a choice.
		got, err = ResolveStore("acme-docs-hanzo", def, def)
		if err != nil || got != def {
			t.Errorf("hyphenated owner naming default %q = %q, %v; want the default", def, got, err)
		}
	}
}

// A store also cannot leave the collection path. The name goes into the search
// backend's URL unescaped, so a slash is the same disclosure by a shorter route.
func TestAStoreCannotLeaveThePath(t *testing.T) {
	for _, bad := range []string{
		"../admin-docs",
		"docs/../../admin",
		"a/b",
		"docs docs",
		"Docs",     // case folds into a different index than it reads as
		"_leading", // must open on a letter or digit
		"a\x00b",
		"docs%2fadmin",
	} {
		if got, err := ResolveStore("acme", bad, "docs"); err == nil {
			t.Errorf("ResolveStore(%q) = %q, want a refusal", bad, got)
		}
	}
}

// The default is OURS — code, not input — so it is taken as given even when it is
// hyphenated, and asking for it by name selects the identical index.
func TestTheDefaultIsOursAndAskingForItIsTheDefault(t *testing.T) {
	for _, def := range []string{"docs-hanzo-ai", DefaultDocsStore, RagFileStore} {
		got, err := ResolveStore("acme", "", def)
		if err != nil || got != def {
			t.Errorf("empty store with default %q = %q, %v; want the default", def, got, err)
		}
		got, err = ResolveStore("acme", def, def)
		if err != nil || got != def {
			t.Errorf("naming default %q = %q, %v; want the default", def, got, err)
		}
	}
	// Naming a DIFFERENT deployment default is not naming this surface's default.
	if _, err := ResolveStore("acme", "rag-files", "docs-hanzo-ai"); err == nil {
		t.Fatal("one surface's default was admitted for another surface")
	}
}

// What a caller may legitimately choose still works.
func TestAPlainStoreIsAdmitted(t *testing.T) {
	for _, ok := range []string{"docs", "docs2", "my_store", "a", "0", DefaultDocsStore} {
		got, err := ResolveStore("acme", ok, "docs-hanzo-ai")
		if err != nil || got != ok {
			t.Errorf("ResolveStore(%q) = %q, %v; want it admitted", ok, got, err)
		}
	}
}

// hyphenatedDefaults are the store names this estate issued before the slug rule
// existed. They are safe ONLY because a hyphenated owner may not choose a store,
// and that guard is the thing keeping them safe — so the set is written down, and
// a new one cannot join it by accident.
var hyphenatedDefaults = map[string]bool{
	"docs-hanzo-ai": true,
	"rag-files":     true,
}

// A NEW HYPHENATED DEFAULT WOULD REOPEN THE CLASS, so adding one has to be a
// decision somebody makes on purpose rather than a literal somebody types.
//
// Every default written as a literal at a call site is checked here: hyphen-free
// is collision-proof on its own, and the two legacy names are admitted by name.
// Anything else fails, and the fix is to spell the new default without a hyphen —
// not to widen the set.
func TestNoNewHyphenatedDefault(t *testing.T) {
	// The constants first, so renaming one to a hyphenated value is caught even
	// though call sites pass it by identifier.
	for name, v := range map[string]string{"DefaultDocsStore": DefaultDocsStore, "RagFileStore": RagFileStore} {
		if strings.Contains(v, "-") && !hyphenatedDefaults[v] {
			t.Errorf("%s = %q introduces a hyphenated default; spell it without a hyphen", name, v)
		}
	}

	found := 0
	for _, dir := range []string{".", "../controllers"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if perr != nil {
				return perr
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isStoreResolver(call.Fun) || len(call.Args) == 0 {
					return true
				}
				found++
				lit, ok := call.Args[len(call.Args)-1].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true // an identifier default; the constants above cover those
				}
				def, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					return true
				}
				if strings.Contains(def, "-") && !hyphenatedDefaults[def] {
					t.Errorf("%s: default %q is hyphenated and not an acknowledged legacy name — "+
						"a hyphenated default lets a hyphenated owner spell another tenant's index", path, def)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	// If the resolver is renamed or moved, this test must fail loudly rather than
	// quietly checking nothing.
	if found == 0 {
		t.Fatal("found no ResolveStore/zapBodyStore call sites; this test has stopped checking anything")
	}
}

// isStoreResolver reports whether a call expression is one of the two functions
// that apply a store default.
func isStoreResolver(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == "ResolveStore" || f.Name == "zapBodyStore"
	case *ast.SelectorExpr:
		return f.Sel.Name == "ResolveStore" || f.Sel.Name == "zapBodyStore"
	}
	return false
}
