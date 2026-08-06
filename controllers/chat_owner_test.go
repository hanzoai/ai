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
	"encoding/json"
	"go/ast"
	"go/token"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestOwnerIsTheLedgerNamespace shows WHY a chat-plane row's Owner is load-bearing
// money: it is the org whose ledger the answer's debit lands in AND the first half
// of the wallet that debit drains. Two messages differing only in Owner charge two
// different tenants.
//
// So Owner is not a label on a row — it is a payment instruction. The tests below
// pin that a request can never write it.
func TestOwnerIsTheLedgerNamespace(t *testing.T) {
	got := captureDebits(t)

	for _, owner := range []string{chatOwner, "victim-org"} {
		err := object.AddTransactionForMessage(&object.Message{
			Owner: owner, Name: "message_1", User: "mallory", Price: 0.5, Currency: "USD",
		})
		if err != nil {
			t.Fatalf("AddTransactionForMessage(%s): %v", owner, err)
		}
	}

	if len(*got) != 2 {
		t.Fatalf("want 2 debits, got %d", len(*got))
	}
	if (*got)[0].namespace != chatOwner || (*got)[0].subject != chatOwner+"/mallory" {
		t.Errorf("owner %q debited %q/%q", chatOwner, (*got)[0].namespace, (*got)[0].subject)
	}
	if (*got)[1].namespace != "victim-org" || (*got)[1].subject != "victim-org/mallory" {
		t.Errorf("owner \"victim-org\" debited %q/%q — Owner must select the ledger, or this test proves nothing", (*got)[1].namespace, (*got)[1].subject)
	}
}

// TestBodyOwnerIsDiscarded proves the client really does put an Owner on the wire:
// the request body unmarshals straight onto the row, Owner included. Nothing in the
// decode step drops it — only the server's own stamp does.
func TestBodyOwnerIsDiscarded(t *testing.T) {
	body := []byte(`{"owner":"victim-org","user":"mallory","name":"message_1","chat":"chat_1","text":"hi"}`)

	var message object.Message
	if err := json.Unmarshal(body, &message); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if message.Owner != "victim-org" {
		t.Fatalf("decoded owner = %q, want the body's %q — the wire carries it", message.Owner, "victim-org")
	}

	// The one statement every chat-plane handler runs before the row is used.
	message.Owner = chatOwner

	if message.Owner != chatOwner {
		t.Fatalf("stamped owner = %q, want %q", message.Owner, chatOwner)
	}
	if message.User != "mallory" {
		t.Errorf("stamping the owner must not disturb the caller's identity, got user %q", message.User)
	}
}

// TestChatPlaneOwnerIsServerStamped is the pin: in every handler that decodes a
// chat-plane row from a request body, the server SETS Owner before anything reads
// it. Honor the body's Owner instead — delete the stamp, or move it after the first
// read — and the handler writes the row, the balance gate and the usage debit into
// a tenant the caller chose, and this fails.
func TestChatPlaneOwnerIsServerStamped(t *testing.T) {
	for _, h := range []struct{ file, handler, row string }{
		{"chat.go", "AddChat", "chat"},
		{"chat.go", "DeleteChat", "chat"},
		{"message.go", "AddMessage", "message"},
		{"message.go", "DeleteMessage", "message"},
		{"message.go", "UpdateMessage", "message"},
	} {
		body := handlerBody(t, h.file, h.handler)

		stamp := ownerStamp(body, h.row)
		if stamp == token.NoPos {
			t.Errorf("%s: never stamps %s.Owner = chatOwner; the request body chooses the tenant", h.handler, h.row)
			continue
		}
		for _, use := range ownerUses(body, h.row, stamp) {
			if use < stamp {
				t.Errorf("%s: reads %s.Owner at pos %d before stamping it at pos %d", h.handler, h.row, use, stamp)
			}
		}
	}
}

// TestChatPlaneIdOwnerIsServerStamped covers the handlers whose row identity comes
// from the ?id= query param instead of the body: the owner half of that id is
// re-derived server-side before the id reaches the object layer, so ?id=victim/x
// addresses nothing rather than another tenant's row.
func TestChatPlaneIdOwnerIsServerStamped(t *testing.T) {
	for _, h := range []struct{ file, handler string }{
		{"chat.go", "UpdateChat"},
		{"message.go", "UpdateMessage"},
	} {
		body := handlerBody(t, h.file, h.handler)

		stamp := idStamp(body)
		if stamp == token.NoPos {
			t.Errorf("%s: never re-derives the id under chatOwner; ?id=victim/x reaches another tenant's row", h.handler)
			continue
		}
		if reached := firstObjectCallWith(body, "id"); reached != token.NoPos && reached < stamp {
			t.Errorf("%s: passes the caller's id to the object layer at pos %d, before re-deriving it at pos %d", h.handler, reached, stamp)
		}
	}
}

// TestDeleteWelcomeMessageIgnoresBodyOwner covers the last shape: the handler builds
// the lookup id itself, and it must build it from chatOwner rather than from the
// owner on the body.
func TestDeleteWelcomeMessageIgnoresBodyOwner(t *testing.T) {
	body := handlerBody(t, "message.go", "DeleteWelcomeMessage")

	if n := countSelectorCalls(body, "util", "GetIdFromOwnerAndName"); n != 1 {
		t.Fatalf("DeleteWelcomeMessage builds %d ids, want exactly 1", n)
	}
	if uses := ownerUses(body, "message", token.NoPos); len(uses) != 0 {
		t.Errorf("DeleteWelcomeMessage still reads message.Owner (%d times); the id must come from chatOwner", len(uses))
	}
	if !usesChatOwner(body) {
		t.Error("DeleteWelcomeMessage never names chatOwner; its lookup id is the caller's")
	}
}

// ownerStamp reports the position of `row.Owner = chatOwner`.
func ownerStamp(n ast.Node, row string) token.Pos {
	found := token.NoPos
	ast.Inspect(n, func(node ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		if !isSelector(assign.Lhs[0], row, "Owner") {
			return true
		}
		if rhs, ok := assign.Rhs[0].(*ast.Ident); ok && rhs.Name == "chatOwner" {
			found = assign.Pos()
			return false
		}
		return true
	})
	return found
}

// ownerUses reports every `row.Owner` position except the stamp's own left side.
func ownerUses(n ast.Node, row string, stamp token.Pos) []token.Pos {
	uses := []token.Pos{}
	ast.Inspect(n, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if ok && assign.Pos() == stamp {
			// Skip the stamp's LHS; walk only its right-hand side.
			for _, rhs := range assign.Rhs {
				ast.Inspect(rhs, func(inner ast.Node) bool {
					if isSelector(inner, row, "Owner") {
						uses = append(uses, inner.Pos())
					}
					return true
				})
			}
			return false
		}
		if isSelector(node, row, "Owner") {
			uses = append(uses, node.Pos())
		}
		return true
	})
	return uses
}

// idStamp reports the position of `id = util.GetIdFromOwnerAndName(chatOwner, ...)`.
func idStamp(n ast.Node) token.Pos {
	found := token.NoPos
	ast.Inspect(n, func(node ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		if lhs, ok := assign.Lhs[0].(*ast.Ident); !ok || lhs.Name != "id" {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == "chatOwner" {
			found = assign.Pos()
			return false
		}
		return true
	})
	return found
}

// firstObjectCallWith reports the first object.X(...) call taking the named ident.
func firstObjectCallWith(n ast.Node, name string) token.Pos {
	found := token.NoPos
	ast.Inspect(n, func(node ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "object" {
			return true
		}
		for _, arg := range call.Args {
			if ident, ok := arg.(*ast.Ident); ok && ident.Name == name {
				found = call.Pos()
				return false
			}
		}
		return true
	})
	return found
}

// usesChatOwner reports whether the node names the chatOwner constant.
func usesChatOwner(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && ident.Name == "chatOwner" {
			found = true
			return false
		}
		return true
	})
	return found
}

// isSelector reports whether the node is exactly `x.field`.
func isSelector(n ast.Node, x, field string) bool {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != field {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == x
}

// TestStoreOwnerIsServerScoped pins the store create to the same rule its own read
// uses. GetStores answers through GetScopedOwner — the caller's own org, overridable
// only by a super admin — while AddStore took the owner off the request body, so an
// admin of ANY org could file a store into a tenant it does not belong to. A store
// filed into the chat plane's tenant is reachable as a default store, and a default
// store names the model every chat answer runs and bills.
func TestStoreOwnerIsServerScoped(t *testing.T) {
	body := handlerBody(t, "store.go", "AddStore")

	stamp := scopedOwnerStamp(body, "store")
	if stamp == token.NoPos {
		t.Fatal("AddStore never scopes store.Owner to GetScopedOwner; the request body chooses the tenant")
	}
	for _, use := range ownerUses(body, "store", stamp) {
		if use < stamp {
			t.Errorf("AddStore reads store.Owner at pos %d before scoping it at pos %d", use, stamp)
		}
	}
	if reached := firstObjectCallWith(body, "store"); reached != token.NoPos && reached < stamp {
		t.Errorf("AddStore hands store to the object layer at pos %d, before scoping its owner at pos %d", reached, stamp)
	}
	// GetStores must still read through the same rule, or the two halves drift apart.
	if !usesScopedOwner(handlerBody(t, "store.go", "GetStores")) {
		t.Error("GetStores no longer reads through GetScopedOwner; the create and the read must share one rule")
	}
}

// scopedOwnerStamp reports the position of `row.Owner = owner` where owner came from
// c.GetScopedOwner().
func scopedOwnerStamp(n ast.Node, row string) token.Pos {
	if !usesScopedOwner(n) {
		return token.NoPos
	}
	found := token.NoPos
	ast.Inspect(n, func(node ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		if !isSelector(assign.Lhs[0], row, "Owner") {
			return true
		}
		if rhs, ok := assign.Rhs[0].(*ast.Ident); ok && rhs.Name == "owner" {
			found = assign.Pos()
			return false
		}
		return true
	})
	return found
}

// usesScopedOwner reports whether the node calls c.GetScopedOwner().
func usesScopedOwner(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "GetScopedOwner" {
			found = true
			return false
		}
		return true
	})
	return found
}
