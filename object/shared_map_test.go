// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A concurrent map write is a Go runtime FATAL ERROR, not a panic: recover() does
// not see it and the process ends. Two of the three process-enders found in this
// module were exactly that — a package map written from one door and read from
// the other, with nothing between them.
//
// Every package-level map written at RUNTIME is therefore written under a lock.
// Reads need no guard when nothing writes, which is why the many lookup tables
// here — stop words, country centroids, route registries — are not in scope: this
// walks the maps something assigns to or deletes from inside a function, and
// checks the function takes a lock.
//
// It checks that a lock is TAKEN, not that it is the right one. The failure this
// exists for is the absence of any lock at all.
func TestEveryMapWrittenAtRuntimeIsGuarded(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	for _, dir := range []string{".", "../controllers", "../i18n", "../model", "../util"} {
		paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		maps := map[string]bool{}
		files := map[string]*ast.File{}
		for _, path := range paths {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			files[path] = file
			for _, name := range packageMapNames(file) {
				maps[name] = true
			}
		}

		unguarded := []string{}
		for _, file := range files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				written := writtenMaps(fn.Body, maps)
				if len(written) == 0 {
					continue
				}
				checked += len(written)
				if takesALock(fn.Body) || saysTheCallerHoldsIt(fn) {
					continue
				}
				for _, name := range written {
					unguarded = append(unguarded, name+" in "+fn.Name.Name+" ("+fset.Position(fn.Pos()).String()+")")
				}
			}
		}
		sort.Strings(unguarded)
		for _, u := range unguarded {
			t.Errorf("%s writes a package map and takes no lock — a concurrent map write "+
				"is a runtime fatal error, which no recover answers for", u)
		}
	}
	if checked == 0 {
		t.Fatal("walked no map writes — the check found nothing to check")
	}
	t.Logf("%d runtime map writes checked", checked)
}

func packageMapNames(file *ast.File) []string {
	names := []string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || !declaresAMap(vs) {
				continue
			}
			for _, n := range vs.Names {
				if n.Name != "_" {
					names = append(names, n.Name)
				}
			}
		}
	}
	return names
}

func declaresAMap(vs *ast.ValueSpec) bool {
	if _, ok := vs.Type.(*ast.MapType); ok {
		return true
	}
	for _, v := range vs.Values {
		switch e := v.(type) {
		case *ast.CompositeLit:
			if _, ok := e.Type.(*ast.MapType); ok {
				return true
			}
		case *ast.CallExpr:
			if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "make" && len(e.Args) > 0 {
				if _, ok := e.Args[0].(*ast.MapType); ok {
					return true
				}
			}
		}
	}
	return false
}

func writtenMaps(body *ast.BlockStmt, maps map[string]bool) []string {
	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		note := func(name string) {
			if maps[name] {
				seen[name] = true
			}
		}
		switch s := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range s.Lhs {
				if idx, ok := lhs.(*ast.IndexExpr); ok {
					if id, ok := idx.X.(*ast.Ident); ok {
						note(id.Name)
					}
				}
			}
		case *ast.CallExpr:
			if id, ok := s.Fun.(*ast.Ident); ok && id.Name == "delete" && len(s.Args) > 0 {
				if m, ok := s.Args[0].(*ast.Ident); ok {
					note(m.Name)
				}
			}
		}
		return true
	})
	out := []string{}
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// saysTheCallerHoldsIt reports whether a function documents that its caller holds
// the lock. Some do — a sweep that is part of admitting decides and writes under
// one lock the caller spans — and taking it again would deadlock. The precondition
// has to be WRITTEN for this to pass, which is the point: an invisible one is
// indistinguishable from none.
func saysTheCallerHoldsIt(fn *ast.FuncDecl) bool {
	if fn.Doc == nil {
		return false
	}
	doc := strings.ToLower(fn.Doc.Text())
	return strings.Contains(doc, "caller holds") || strings.Contains(doc, "caller must hold")
}

func takesALock(body *ast.BlockStmt) bool {
	locked := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "Lock" || sel.Sel.Name == "RLock" {
				locked = true
			}
		}
		return true
	})
	return locked
}
