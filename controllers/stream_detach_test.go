// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// A streamed body is produced by fasthttp draining the writer from ITS OWN
// goroutine, after the handler has returned and fiber has released the request
// context. Two things follow, and both are why this test exists rather than a
// comment: reading through the controller there dereferences a released context,
// and the panic that follows is in a goroutine the router's recover never sees —
// so it ends the process rather than the request.
//
// Everything a stream writer needs is therefore taken while the request is still
// ours (takeSnapshot) and carried in. This walks every inline closure handed to
// SendStreamWriter and fails on any use of the controller inside one.
//
// It checks the inline form only. A named function passed by name — anthropic's
// run — serves the streamed and un-streamed cases from one body, so a controller
// call in it may be correct; those route through a fail() that knows which
// connection is open, which is a claim about a branch that no walk of the syntax
// can settle.
func TestNoStreamWriterReachesThroughTheController(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "SendStreamWriter" {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			lit, ok := call.Args[0].(*ast.FuncLit)
			if !ok {
				return true // passed by name; see the note above
			}
			checked++
			ast.Inspect(lit.Body, func(m ast.Node) bool {
				inner, ok := m.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := inner.X.(*ast.Ident); ok && id.Name == recv.Name {
					t.Errorf("%s: the stream writer uses %s.%s — the request context is "+
						"released by the time it runs; take it from a snapshot before "+
						"handing the closure over", fset.Position(inner.Pos()), recv.Name, inner.Sel.Name)
				}
				return true
			})
			return true
		})
	}
	if checked == 0 {
		t.Fatal("walked no stream writers — the check found nothing to check")
	}
	t.Logf("%d inline stream writers checked", checked)
}
