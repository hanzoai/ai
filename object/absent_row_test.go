// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A row that is not there is (nil, nil) in this store: the value and the error
// are nil together, so absence never arrives as an error. Every caller has to say
// what it does about that, and the ones that did not were the sharpest failures
// in this module — a dereference inside a hijacked connection or a stream writer
// is a goroutine's panic, which ends the PROCESS rather than the request.
//
// This walks every reader of such a function and fails on one that dereferences
// the value with no nil check between.
func TestNoAbsentRowIsDereferenced(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, dir := range []string{".", "../controllers", "../cluster"} {
		matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range matches {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			files[path] = file
		}
	}

	// Which functions answer (nil, nil), and which names more than one type
	// declares. A name two receivers share cannot be resolved by reading the
	// syntax, so it is not checked — and the set of them is asserted below, so a
	// NEW collision has to be named rather than quietly widening the blind spot.
	absent := map[string]bool{}
	receivers := map[string]map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			if receivers[fn.Name.Name] == nil {
				receivers[fn.Name.Name] = map[string]bool{}
			}
			// A plain function is recorded under the empty receiver, so a name shared
			// between a function and a method counts as shared too.
			owner := ""
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				owner = typeName(fn.Recv.List[0].Type)
			}
			receivers[fn.Name.Name][owner] = true
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				ret, ok := m.(*ast.ReturnStmt)
				if !ok || len(ret.Results) != 2 {
					return true
				}
				a, aok := ret.Results[0].(*ast.Ident)
				b, bok := ret.Results[1].(*ast.Ident)
				if aok && bok && a.Name == "nil" && b.Name == "nil" {
					absent[fn.Name.Name] = true
				}
				return true
			})
			return true
		})
	}

	shared := []string{}
	for name, types := range receivers {
		if len(types) > 1 && absent[name] {
			shared = append(shared, name)
		}
	}
	sort.Strings(shared)
	// These four are a store function and the HTTP handler that mirrors it —
	// object.GetRecord and ApiController.GetRecord — which is how this module names
	// a handler on purpose. Reading the syntax cannot tell one call from the other,
	// so they sit outside the check; a NEW shared name surfaces here rather than
	// quietly widening the blind spot.
	knownShared := []string{"GetFinetuneJob", "GetModelRoute", "GetModelRoutes", "GetRecord"}
	if strings.Join(shared, ",") != strings.Join(knownShared, ",") {
		t.Errorf("the set of names two types share has changed:\n  now:  %v\n  was:  %v\n"+
			"a shared name cannot be resolved by reading the syntax, so it is not checked — "+
			"give the new one a name of its own, or add it here deliberately", shared, knownShared)
	}
	for _, name := range knownShared {
		delete(absent, name)
	}

	for path, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			body, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, stmt := range body.List {
				as, ok := stmt.(*ast.AssignStmt)
				if !ok || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
					continue
				}
				call, ok := as.Rhs[0].(*ast.CallExpr)
				if !ok {
					continue
				}
				name := calledName(call.Fun)
				if name == "" || !absent[name] {
					continue
				}
				val, ok := as.Lhs[0].(*ast.Ident)
				if !ok || val.Name == "_" {
					continue
				}
				if where := derefBeforeCheck(body.List[i+1:], val.Name); where != token.NoPos {
					t.Errorf("%s: %s answers (nil, nil) for a row that is not there, and %s is "+
						"read at %s with nothing between saying what happens when it is absent",
						path, name, val.Name, fset.Position(where))
				}
			}
			return true
		})
	}
}

// derefBeforeCheck reports where name is first read as a value, if that happens
// before anything compares it to nil.
func derefBeforeCheck(rest []ast.Stmt, name string) token.Pos {
	for _, stmt := range rest {
		checked, deref := false, token.NoPos
		ast.Inspect(stmt, func(m ast.Node) bool {
			if bin, ok := m.(*ast.BinaryExpr); ok {
				if x, ok := bin.X.(*ast.Ident); ok && x.Name == name {
					if y, ok := bin.Y.(*ast.Ident); ok && y.Name == "nil" {
						checked = true
					}
				}
			}
			if sel, ok := m.(*ast.SelectorExpr); ok && deref == token.NoPos {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == name {
					deref = sel.Pos()
				}
			}
			return true
		})
		if checked {
			return token.NoPos
		}
		if deref != token.NoPos {
			return deref
		}
	}
	return token.NoPos
}

func calledName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
}

func typeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return typeName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return fmt.Sprintf("%T", expr)
}
