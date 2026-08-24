// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A ZAP handler that stores a row says whose row it is.
//
// These take the row off the body, owner field and all, and the listings beside
// them are scoped — so without this a row is written where no listing would show
// it. The HTTP surface learned the same rule; this is what keeps the two from
// drifting apart again, table by table.
//
// What counts as saying so: stamping the owner (theirOrg / themselves), refusing
// an id out of reach (zapReachable), resolving the row first (storeFor,
// zapKSFVScopedOwner), or gating the whole handler on the platform admin — which
// is a different control and a sufficient one.
func TestEveryZapWriteSaysWhoseRowItIs(t *testing.T) {
	// The mechanisms that answer it. Each is a real one, not a spelling: stamp the
	// owner, refuse an id out of reach, resolve the row, gate on the platform admin,
	// check the row names the caller, or derive the scope from the principal.
	says := regexp.MustCompile(`theirOrg\(|themselves\(|zapReachable\(|storeFor\(|` +
		`zapKSFVScopedOwner\(|SuperAdmin\(|zapWrite\(|zapIsCurrentUser\(|` +
		`zapMemoryIdentity\(|zapRPSOrg\(|user\.Owner|user\.Name|sa\.Owner`)
	writes := regexp.MustCompile(`object\.(Add|Update|Delete)\w+\(`)

	// Handlers that store a row and answer for it another way. Each is here
	// because of what it is, not because nobody got to it.
	named := map[string]string{
		// Keyed by (store, key) rather than by an owner, and identical on both
		// surfaces — whether a store may be written to is its own question.
		"zapAddTreeFileHandler":    "keyed by store and key, not by an owner",
		"zapUpdateTreeFileHandler": "keyed by store and key, not by an owner",
		"zapDeleteTreeFileHandler": "keyed by store and key, not by an owner",
		// Gated on the platform admin through zapMiscSuperAdmin — a map this check
		// cannot see, because the gate is a lookup rather than a call. Naming any
		// organization's settings is what that endpoint is for.
		"zapOrgSettingsHandler": "gated on the platform admin, by a map rather than a call",
	}

	fset := token.NewFileSet()
	paths, err := filepath.Glob("zap_*.go")
	if err != nil {
		t.Fatal(err)
	}
	open, checked := []string{}, 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		src, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = src
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasSuffix(fn.Name.Name, "Handler") {
				continue
			}
			body := source(t, fset, fn)
			if !writes.MatchString(body) {
				continue
			}
			checked++
			if _, ours := named[fn.Name.Name]; ours {
				continue
			}
			if !says.MatchString(body) {
				open = append(open, fn.Name.Name)
			}
		}
	}
	sort.Strings(open)
	for _, name := range open {
		t.Errorf("%s stores a row without saying whose it is — stamp the owner, refuse "+
			"an id out of reach, or name it in this test with the reason", name)
	}
	if checked == 0 {
		t.Fatal("found no write handlers")
	}
	t.Logf("%d write handlers checked", checked)
}

// source renders a function back to text, which is what the checks above read.
func source(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) string {
	t.Helper()
	start, end := fset.Position(fn.Pos()), fset.Position(fn.End())
	raw, err := os.ReadFile(start.Filename)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw[start.Offset:end.Offset])
}
