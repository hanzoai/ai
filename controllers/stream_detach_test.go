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

// Two callbacks in this package run after the handler has returned: the one
// fasthttp drains a streamed body from, and the one it hands a hijacked socket
// to. Both run in a goroutine of their own, on a request context fiber has
// already released. So reading through the controller in either dereferences
// nothing, and the panic that follows is in a goroutine the router's recover
// never sees — it ends the process rather than the request.
//
// Everything such a callback needs is therefore read while the request is still
// ours and carried in. This walks every inline closure handed to one of them and
// fails on any use of the controller inside.
//
// It checks the inline form only. A function passed by NAME — anthropic's run —
// serves the streamed and un-streamed cases from one body, so a controller call
// in it may well be correct; those route through a fail() that knows which
// connection is still open, and which branch a call sits in is not something a
// walk of the syntax can settle.
func TestNoCallbackOutlivingItsRequestReachesThroughTheController(t *testing.T) {
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
			if !ok {
				return true
			}
			// The hijack arm: UpGrader.Upgrade(ctx, func(ws *websocket.Conn) {…}).
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Upgrade" && len(call.Args) == 2 {
				if lit, ok := call.Args[1].(*ast.FuncLit); ok {
					checked++
					reachThrough(t, fset, lit.Body, "c")
				}
				return true
			}
			if len(call.Args) != 1 {
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
			reachThrough(t, fset, lit.Body, recv.Name)
			return true
		})
	}
	if checked == 0 {
		t.Fatal("walked no callbacks — the check found nothing to check")
	}
	t.Logf("%d callbacks checked", checked)
}

// reachThrough reports every use of the named receiver inside a callback body.
func reachThrough(t *testing.T, fset *token.FileSet, body *ast.BlockStmt, recv string) {
	t.Helper()
	ast.Inspect(body, func(m ast.Node) bool {
		sel, ok := m.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
			t.Errorf("%s: this callback uses %s.%s, and the request context is released "+
				"by the time it runs — read it before handing the closure over",
				fset.Position(sel.Pos()), recv, sel.Sel.Name)
		}
		return true
	})
}
