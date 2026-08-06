// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"go/ast"
	"go/token"
	"testing"
)

// TestMessageAnswerRefusesAMissingChat pins the nil guard on GetMessageAnswer's
// chat.
//
// object.GetChat reports a miss as (nil, nil) — the id matched no row is not an
// error — so the miss reaches the handler as a nil chat that the error branch above
// it does not catch. Every field read off it panics the process, and a message CAN
// outlive its chat: DeleteChat cascades the message delete keyed on the chat's own
// owner, so a message stored under any other owner survives as an orphan whose
// answer is one unauthenticated request away.
//
// The handler cannot be driven here (it reads rows from the first line), and the
// invariant is positional anyway: the guard must come BEFORE the first field read.
// Deleting the guard puts the read first and this fails.
func TestMessageAnswerRefusesAMissingChat(t *testing.T) {
	body := handlerBody(t, "message_answer.go", "GetMessageAnswer")

	guard := firstNilCheck(body, "chat")
	if guard == token.NoPos {
		t.Fatal("GetMessageAnswer never checks chat == nil; a missing chat panics the process")
	}
	read := firstFieldRead(body, "chat")
	if read == token.NoPos {
		t.Fatal("GetMessageAnswer never reads a field off chat — the guard under test is moot")
	}
	if guard > read {
		t.Errorf("GetMessageAnswer reads a chat field at pos %d before checking chat == nil at pos %d", read, guard)
	}
}

// firstNilCheck reports the position of the first `name == nil` comparison.
func firstNilCheck(n ast.Node, name string) token.Pos {
	found := token.NoPos
	ast.Inspect(n, func(node ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		cmp, ok := node.(*ast.BinaryExpr)
		if !ok || cmp.Op != token.EQL {
			return true
		}
		x, ok := cmp.X.(*ast.Ident)
		if !ok || x.Name != name {
			return true
		}
		if y, ok := cmp.Y.(*ast.Ident); ok && y.Name == "nil" {
			found = cmp.Pos()
			return false
		}
		return true
	})
	return found
}

// firstFieldRead reports the position of the first `name.Field` selector.
func firstFieldRead(n ast.Node, name string) token.Pos {
	found := token.NoPos
	ast.Inspect(n, func(node ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == name {
			found = sel.Pos()
			return false
		}
		return true
	})
	return found
}
