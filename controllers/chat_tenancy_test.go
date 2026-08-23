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
	"context"
	"go/ast"
	"go/token"
	"net/http"
	"testing"

	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// chatPlane stands up an in-memory chat plane and returns a reader for a chat under
// the server's own owner. Both planes' write handlers are driven against it, so what
// these tests assert is what actually landed in the store — not what a handler said.
func chatPlane(t *testing.T, dsn string) func(name string) *object.Chat {
	t.Helper()
	restore, err := object.UseMemoryDB(dsn, &object.Chat{}, &object.Message{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)
	// AddChat stamps a user-agent descriptor, which boot's InitParser makes possible.
	// Stand the plane up the way boot does, or the real handler nil-derefs on a path
	// production never takes and the test measures the wrong thing.
	return func(name string) *object.Chat {
		got, err := object.GetChat(util.GetIdFromOwnerAndName(chatOwner, name))
		if err != nil {
			t.Fatalf("GetChat(%s): %v", name, err)
		}
		return got
	}
}

// seedChat puts one chat belonging to `user` into the plane.
func seedChat(t *testing.T, name, user, org string) {
	t.Helper()
	ok, err := object.AddChat(&object.Chat{
		Owner: chatOwner, Name: name, User: user, Organization: org,
		Store: "store_1", Type: "AI", CreatedTime: util.GetCurrentTime(),
	})
	if err != nil || !ok {
		t.Fatalf("seed chat %s: ok=%v err=%v", name, ok, err)
	}
}

// post builds a controller on a chat-plane write body, with an optional signed-in
// session user — nil is an anonymous caller, which is what these routes actually
// serve (they are on the authz filter's benign-read exempt list).
func post(t *testing.T, path, body string, user *iam.User) *ApiController {
	t.Helper()
	// A nil user is a caller who presents nothing, which several of these tests are
	// about — so it must not be minted a credential.
	auth := ""
	if user != nil {
		auth = authtest.Bearer(t, *user)
	}
	c := presenting(visit(http.MethodPost, path), auth)
	c.Fiber().Request().SetBody([]byte(body))
	return c
}

// ── RED-2: the empty user is not a user ──────────────────────────────────────

// TestIsCurrentUserRefusesANonName is the the router half of the empty-user hole. An
// unauthenticated request has no session username, so the ownership guard compared
// "" against the body's "" and returned ALLOWED — every anonymous caller was the
// current user of the empty user.
//
// That is money, not a label. AddTransactionForMessage builds the subject it debits
// by joining a chat-plane row's Owner to its User, so a row with no user bills
// `admin/`: the admin ORG's own pool wallet, which is Hanzo's platform account rather
// than any customer's. The "/" case is the same hole from the other side — the join
// is skipped for a user that already contains one, so "victim-org/bob" is passed
// through whole and names that tenant's wallet directly.
//
// Drop the isName guard from IsCurrentUser and the first two cases return ALLOWED.
func TestIsCurrentUserRefusesANonName(t *testing.T) {
	admin := &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}
	alice := &iam.User{Owner: "acme", Name: "alice"}

	for _, tc := range []struct {
		what  string
		user  *iam.User
		input string
	}{
		{"an anonymous caller acting as the empty user", nil, ""},
		{"an admin acting as the empty user", admin, ""},
		{"an admin acting as a whole billing subject", admin, "victim-org/bob"},
		{"a user acting as a whole billing subject", alice, "victim-org/bob"},
	} {
		if post(t, "/v1/add-chat", "{}", tc.user).IsCurrentUser(tc.input) {
			t.Errorf("%s was allowed; %q is not a user this plane can bill", tc.what, tc.input)
		}
	}

	// The other side of the guard: it refuses non-names and nothing else.
	if !post(t, "/v1/add-chat", "{}", admin).IsCurrentUser("alice") {
		t.Error("an admin was refused acting on a named user's row")
	}
	if !post(t, "/v1/add-chat", "{}", alice).IsCurrentUser("alice") {
		t.Error("a user was refused acting on its own row")
	}
}

// TestZapIsCurrentUserRefusesANonName is the SAME hole on the ZAP twin plane, where
// it is worse: zapPrincipal("") resolves to nil for a caller with no credential
// at all, so username was "" and the guard compared "" against a body-supplied "".
// The existing ZapIsCurrentUser test never passed "" and so never saw it.
//
// Drop the isName guard from zapIsCurrentUser and the first two cases return nil
// (allowed) for an unauthenticated caller.
func TestZapIsCurrentUserRefusesANonName(t *testing.T) {
	admin := &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}
	alice := &iam.User{Owner: "acme", Name: "alice"}

	for _, tc := range []struct {
		what  string
		user  *iam.User
		input string
	}{
		{"an unauthenticated caller acting as the empty user", nil, ""},
		{"an admin acting as the empty user", admin, ""},
		{"an admin acting as a whole billing subject", admin, "victim-org/bob"},
		{"a user acting as a whole billing subject", alice, "victim-org/bob"},
	} {
		deny := zapIsCurrentUser(tc.user, tc.input)
		if deny == nil {
			t.Errorf("%s was allowed; %q is not a user this plane can bill", tc.what, tc.input)
			continue
		}
		if got := deny.Root().Uint32(object.CloudRespStatus); got != 403 {
			t.Errorf("%s denied with %d, want 403", tc.what, got)
		}
	}

	if deny := zapIsCurrentUser(admin, "alice"); deny != nil {
		t.Error("an admin was refused acting on a named user's row")
	}
	if deny := zapIsCurrentUser(alice, "alice"); deny != nil {
		t.Error("a user was refused acting on its own row")
	}
}

// ── RED-1: a request may not name the ledger it spends from ──────────────────

// TestZapChatWriteCannotReachAForeignLedger drives the REGISTERED native handlers the
// way the wire reaches them: MsgType 100, no credential, a body naming another
// tenant's owner. These two methods are on the benign-read exempt list, so the outer
// org-admin gate does not stop them — the handler's own ownership check is the only
// thing between an anonymous caller and a chat-plane row under `victim-org`, whose
// answer debits victim-org's ledger.
//
// It asserts the STORE, not just the status: nothing may land under the owner the
// request named. Revert zapIsCurrentUser and both writes are admitted.
func TestZapChatWriteCannotReachAForeignLedger(t *testing.T) {
	chatPlane(t, "file:zapforeignledger?mode=memory&cache=shared")

	for _, tc := range []struct{ method, body string }{
		{"chats.add", `{"owner":"victim-org","name":"chat_x","user":"","store":"store_1","type":"AI"}`},
		{"messages.add", `{"owner":"victim-org","name":"message_x","user":"","chat":"chat_x","text":"drain"}`},
		{"chats.delete", `{"owner":"victim-org","name":"chat_x","user":""}`},
		{"messages.update", `{"id":"victim-org/message_x","message":{"owner":"victim-org","name":"message_x","user":""}}`},
	} {
		h, ok := lookupCloudHandler(tc.method)
		if !ok {
			t.Fatalf("method %q not registered", tc.method)
		}
		msg, err := h(context.Background(), "", []byte(tc.body))
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", tc.method, err)
		}
		if got := msg.Root().Uint32(object.CloudRespStatus); got != 403 {
			t.Errorf("%s: an unauthenticated write naming owner victim-org answered %d, want 403", tc.method, got)
		}
	}

	if got, _ := object.GetChat("victim-org/chat_x"); got != nil {
		t.Error("a chat landed under the owner the REQUEST named — its answers debit that tenant's ledger")
	}
	if got, _ := object.GetMessage("victim-org/message_x"); got != nil {
		t.Error("a message landed under the owner the REQUEST named — that row is what gets priced and debited")
	}
}

// TestZapChatPlaneOwnerIsServerStamped is the other half of the same property, for
// the caller the behaviour test above cannot mint: one with a VALID Bearer. An
// authenticated tenant passes its own name in `user` and any owner it likes in
// `owner`, so the ownership check admits it and only the server's stamp decides the
// tenant. This pins that the stamp is there and that nothing reads the body's owner
// before it — the same assertion chat_owner_test.go makes for the controller twins,
// against the ZAP file that mirrors them.
func TestZapChatPlaneOwnerIsServerStamped(t *testing.T) {
	const file = "zap_chat-graph-crud.go"

	for _, h := range []struct{ handler, row string }{
		{"zapAddChatHandler", "chat"},
		{"zapDeleteChatHandler", "chat"},
		{"zapAddMessageHandler", "message"},
		{"zapUpdateMessageHandler", "message"},
		{"zapDeleteMessageHandler", "message"},
	} {
		body := handlerBody(t, file, h.handler)

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

	// The two id-driven handlers carry no row to stamp: their tenant comes from the
	// caller's `id`, so the owner half of it is re-derived before the object layer
	// sees it, and ?id=victim/x addresses nothing rather than someone else's row.
	for _, handler := range []string{"zapUpdateChatHandler", "zapUpdateMessageHandler"} {
		body := handlerBody(t, file, handler)
		if idStamp(body) == token.NoPos {
			t.Errorf("%s: never re-derives the id under chatOwner; id=victim/x reaches another tenant's row", handler)
		}
	}

	// And the handler that builds its own lookup id must build it from chatOwner.
	body := handlerBody(t, file, "zapDeleteWelcomeMessageHandler")
	if uses := ownerUses(body, "reqMessage", token.NoPos); len(uses) != 0 {
		t.Errorf("zapDeleteWelcomeMessageHandler still reads the body's Owner (%d times); its lookup id must come from chatOwner", len(uses))
	}
	if !usesChatOwner(body) {
		t.Error("zapDeleteWelcomeMessageHandler never names chatOwner; its lookup id is the caller's")
	}
}

// ── RED-4: Organization is the usage plane's org key ─────────────────────────

// TestAddChatPinsOwnerAndOrganization drives the real the router AddChat and reads back
// what landed. Owner selects the ledger an answer's debit lands in; Organization is
// the org the usage plane books that turn to (recordCasibaseChatUsage and both TTS
// recorders read it, falling back to Owner). Both were the caller's to choose.
//
// Delete either stamp in AddChat and the corresponding assertion fails against a row
// that says "victim-org".
func TestAddChatPinsOwnerAndOrganization(t *testing.T) {
	read := chatPlane(t, "file:addchatpin?mode=memory&cache=shared")

	alice := &iam.User{Owner: "acme", Name: "alice"}
	body := `{"owner":"victim-org","organization":"victim-org","name":"chat_1","user":"alice","store":"store_1","type":"AI"}`
	post(t, "/v1/add-chat", body, alice).AddChat()

	got := read("chat_1")
	if got == nil {
		t.Fatal("no chat landed under the server's own owner; AddChat did not write the row it accepted")
	}
	if got.Owner != chatOwner {
		t.Errorf("stored owner = %q, want %q — Owner names the ledger the answer's debit lands in", got.Owner, chatOwner)
	}
	if got.Organization != "acme" {
		t.Errorf("stored organization = %q, want the principal's own org %q — Organization is the org the usage plane books this chat's turns to", got.Organization, "acme")
	}
	if victim, _ := object.GetChat("victim-org/chat_1"); victim != nil {
		t.Error("the owner the REQUEST named reached the store")
	}
}

// TestUpdateChatCannotMoveAChatBetweenOrgs is the update side: a stored chat's org is
// not a field a later request may rewrite, or a caller books its own spend to a
// tenant it does not belong to.
func TestUpdateChatCannotMoveAChatBetweenOrgs(t *testing.T) {
	read := chatPlane(t, "file:updatechatorg?mode=memory&cache=shared")
	seedChat(t, "chat_1", "alice", "acme")

	alice := &iam.User{Owner: "acme", Name: "alice"}
	c := post(t, "/v1/update-chat", `{"organization":"victim-org","name":"chat_1","user":"alice","displayName":"renamed"}`, alice)
	c.Fiber().Request().URI().SetQueryString("id=admin/chat_1")
	c.UpdateChat()

	got := read("chat_1")
	if got == nil {
		t.Fatal("the chat vanished")
	}
	if got.Organization != "acme" {
		t.Errorf("stored organization = %q, want %q — an update may not move a chat to another tenant's books", got.Organization, "acme")
	}
	if got.DisplayName != "renamed" {
		t.Errorf("the caller's own field was not written (displayName = %q); the pin must refuse the tenant, not the update", got.DisplayName)
	}
}

// ── RED-3: a delete is authorized by the row, not by the request ─────────────

// TestDeleteChatCannotDestroyAForeignRow covers both shapes of the same mistake. The
// body names WHICH chat (owner + name) and, separately, a `user` the caller also
// chose — so authorizing on that field asks the attacker whether the attack is
// allowed. Only the STORED row knows whose chat it is.
//
// Case (a) is the anonymous one: it passed because IsCurrentUser("") was true.
// Case (b) is the one that survives that fix: Mallory signs in, names ITSELF in
// `user`, and names Alice's chat in `name`. Remove the stored-row check in DeleteChat
// and (b) destroys the chat and every message in it.
func TestDeleteChatCannotDestroyAForeignRow(t *testing.T) {
	read := chatPlane(t, "file:deletechatforeign?mode=memory&cache=shared")
	seedChat(t, "chat_victim", "alice", "acme")

	post(t, "/v1/delete-chat", `{"owner":"admin","name":"chat_victim","user":""}`, nil).DeleteChat()
	if read("chat_victim") == nil {
		t.Fatal("an anonymous caller destroyed another user's chat")
	}

	mallory := &iam.User{Owner: "acme", Name: "mallory"}
	post(t, "/v1/delete-chat", `{"owner":"admin","name":"chat_victim","user":"mallory"}`, mallory).DeleteChat()
	if read("chat_victim") == nil {
		t.Fatal("a signed-in caller naming ITSELF in `user` destroyed a chat owned by someone else")
	}

	// The owner may still delete her own, so the guard refuses the attack and nothing else.
	alice := &iam.User{Owner: "acme", Name: "alice"}
	post(t, "/v1/delete-chat", `{"owner":"admin","name":"chat_victim","user":"alice"}`, alice).DeleteChat()
	if read("chat_victim") != nil {
		t.Error("the chat's own user was refused deleting it")
	}
}

// TestUpdateChatCannotOverwriteAForeignRow is the same property on the id-driven
// handler: ?id= names the row, the body names a user, and only the stored row is
// evidence of whose chat it is.
func TestUpdateChatCannotOverwriteAForeignRow(t *testing.T) {
	read := chatPlane(t, "file:updatechatforeign?mode=memory&cache=shared")
	seedChat(t, "chat_victim", "alice", "acme")

	mallory := &iam.User{Owner: "acme", Name: "mallory"}
	c := post(t, "/v1/update-chat", `{"name":"chat_victim","user":"mallory","displayName":"owned"}`, mallory)
	c.Fiber().Request().URI().SetQueryString("id=admin/chat_victim")
	c.UpdateChat()

	got := read("chat_victim")
	if got == nil {
		t.Fatal("the chat vanished")
	}
	if got.DisplayName == "owned" || got.User == "mallory" {
		t.Errorf("a signed-in caller overwrote a chat owned by someone else: user=%q displayName=%q", got.User, got.DisplayName)
	}
}

// ── RED-7 / RED-11: termination, and the misses that used to panic ───────────

// TestTheClaimSettlesBeforeTheDebit pins the ORDER, which is the whole point of
// settling being its own write. The handler charges the turn and then persists the
// answered row; a persist that fails in between used to leave the message textless
// and unclaimed, which is exactly the state the next request generates and charges
// again. Recording termination FIRST makes that window closed rather than narrow.
//
// Move the settle below the debit and this fails.
func TestTheClaimSettlesBeforeTheDebit(t *testing.T) {
	body := handlerBody(t, "message_answer.go", "GetMessageAnswer")

	settle := firstCall(body, "object", "SettleMessageAnswer")
	if settle == token.NoPos {
		t.Fatal("GetMessageAnswer never settles the claim; an empty completion is generated and charged again on every later request")
	}
	debit := firstCall(body, "object", "AddTransactionForMessage")
	if debit == token.NoPos {
		t.Fatal("GetMessageAnswer no longer debits — this test is measuring the wrong function")
	}
	if debit < settle {
		t.Errorf("the debit runs at pos %d, before the claim settles at pos %d — a persist that fails after the charge lands leaves the turn chargeable again", debit, settle)
	}
}

// TestDeleteWelcomeMessageSurvivesAMissingRow is the the router half of the unauthenticated
// nil-deref class. GetMessage answers (nil, nil) for an id no row matches, so a miss
// arrives as a nil message and not as an error, and this handler then read message.User
// off it. The route serves callers with no session at all, so that is a crash anyone
// can trigger with one request.
func TestDeleteWelcomeMessageSurvivesAMissingRow(t *testing.T) {
	chatPlane(t, "file:welcomemissing?mode=memory&cache=shared")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("an unauthenticated request naming a message that does not exist panicked the process: %v", r)
		}
	}()
	post(t, "/v1/delete-welcome-message", `{"owner":"admin","name":"message_nope"}`, nil).DeleteWelcomeMessage()
}

// firstCall reports the position of the first pkg.Name(...) call inside n.
func firstCall(n ast.Node, pkg, name string) token.Pos {
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
		if !ok || sel.Sel.Name != name {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == pkg {
			found = call.Pos()
			return false
		}
		return true
	})
	return found
}

// ── registerCloud audit: the one other twin carrying this same drift ─────────

// TestZapAddStoreScopesTheOwnerToThePrincipal covers the single remaining DRIFT the
// registerCloud sweep found: the the router AddStore was hardened to resolve the owner
// through GetScopedOwner, and its ZAP twin still took it from the body. It is the
// same cross-tenant drain as the chat plane and reachable by ANY signed-in tenant —
// a store filed into the chat plane's own org is reachable as a DEFAULT store, and a
// default store names the model every chat answer runs and bills.
//
// The handler must resolve scope before the row is used, so an unauthenticated caller
// is refused at the same seam. Delete the zapKSFVScopedOwner stamp and the store lands
// under the owner the body named.
func TestZapAddStoreScopesTheOwnerToThePrincipal(t *testing.T) {
	h, ok := lookupCloudHandler("stores.add")
	if !ok {
		t.Fatal("stores.add not registered")
	}
	msg, err := h(context.Background(), "", []byte(`{"owner":"admin","name":"store_x"}`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 401 {
		t.Errorf("an unauthenticated stores.add naming owner admin answered %d, want 401", got)
	}

	// And the stamp itself: the row's owner comes from the scope seam, before the
	// store is used — the assertion the AST pin makes for the chat plane's twins.
	body := handlerBody(t, "zap_knowledge-stores-files-vectors.go", "zapAddStoreHandler")
	stamp := scopedOwnerStamp(body, "store")
	if stamp == token.NoPos {
		t.Fatal("zapAddStoreHandler never scopes store.Owner to zapKSFVScopedOwner; the request body chooses the tenant")
	}
	if reached := firstObjectCallWith(body, "store"); reached != token.NoPos && reached < stamp {
		t.Errorf("zapAddStoreHandler hands store to the object layer at pos %d, before scoping its owner at pos %d", reached, stamp)
	}
	// The body's owner may be READ exactly once before the stamp — as the argument the
	// scope seam judges. Any other read is the un-scoped tenant leaking downstream.
	early := 0
	for _, use := range ownerUses(body, "store", stamp) {
		if use < stamp {
			early++
		}
	}
	if early > 1 {
		t.Errorf("zapAddStoreHandler reads the body's store.Owner %d times before scoping it; only the scope seam's own argument may", early)
	}
}
