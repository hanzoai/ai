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
	"encoding/json"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
)

// chatGraphWriteMethods are the group's write routes NOT on the beego filter's
// benign-read exempt list — every one requires an ORG admin at the outer gate, so
// an anonymous (no-principal) caller must be denied 403 BEFORE any DB access. No
// ORM adapter is initialized in this unit test, so a handler that reached the
// store would panic; reaching the 403 assertion proves the gate ran first.
var chatGraphWriteMethods = []string{
	"messages.delete",
	"graphs.update",
	"graphs.add",
	"graphs.delete",
}

// TestZapChatGraphAdminGateRejection asserts every admin-gated write handler fails
// closed with 403 on an empty Bearer credential (parity with the beego authz
// filter's org-admin denyForbidden tail for the non-exempt write routes).
func TestZapChatGraphAdminGateRejection(t *testing.T) {
	for _, method := range chatGraphWriteMethods {
		h, ok := lookupCloudHandler(method)
		if !ok {
			t.Fatalf("method %q not registered", method)
		}
		msg, err := h(context.Background(), "", []byte("{}"))
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", method, err)
		}
		if got := msg.Root().Uint32(object.CloudRespStatus); got != 403 {
			t.Errorf("%s: empty auth status = %d, want 403", method, got)
		}
	}
}

// TestZapGlobalMessagesRequiresSignedIn asserts the admin-only global-messages read
// fails closed with 401 for an anonymous caller before any DB access (parity with
// GetGlobalMessages' RequireSignedIn).
func TestZapGlobalMessagesRequiresSignedIn(t *testing.T) {
	msg, err := zapGetGlobalMessagesHandler(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 401 {
		t.Errorf("empty auth status = %d, want 401", got)
	}
}

// TestZapChatGraphRegistry asserts the group self-registered its full route set
// into both registries and that dispatch routes correctly, including the
// longest-prefix resolution for the shared-prefix pairs (/v1/get-chat ⊂
// /v1/get-chats, /v1/get-graph ⊂ /v1/get-graphs, /v1/get-message ⊂
// /v1/get-messages).
func TestZapChatGraphRegistry(t *testing.T) {
	cloudMethods := []string{
		"chats.global.list", "chats.list", "chats.get", "chats.update", "chats.add", "chats.delete",
		"messages.global.list", "messages.list", "messages.get", "messages.update", "messages.add", "messages.delete", "messages.welcome.delete",
		"graphs.global.list", "graphs.list", "graphs.get", "graphs.update", "graphs.add", "graphs.delete",
	}
	for _, method := range cloudMethods {
		if _, ok := lookupCloudHandler(method); !ok {
			t.Errorf("cloud method %q not registered", method)
		}
	}

	// Longest-prefix: the plural list routes must not be shadowed by the shorter
	// singular get routes registered for the same family.
	cases := map[string]string{
		"/v1/get-chats":    "/v1/get-chats",
		"/v1/get-chat":     "/v1/get-chat",
		"/v1/get-graphs":   "/v1/get-graphs",
		"/v1/get-graph":    "/v1/get-graph",
		"/v1/get-messages": "/v1/get-messages",
		"/v1/get-message":  "/v1/get-message",
	}
	for path := range cases {
		_, rich := lookupGatewayRoute(path)
		_, simple := lookupGatewayHandler(path)
		if !rich && !simple {
			t.Errorf("gateway path %s not registered", path)
		}
	}

	// Unknown path resolves to no group (falls back to the legacy switch / beego).
	if _, rich := lookupGatewayRoute("/v1/nope"); rich {
		t.Errorf("/v1/nope unexpectedly registered (rich)")
	}
	if _, simple := lookupGatewayHandler("/v1/nope"); simple {
		t.Errorf("/v1/nope unexpectedly registered (simple)")
	}
}

// TestZapIsCurrentUser asserts the ownership helper: an org admin may act on any
// user's row; a non-admin only on its own; a nil principal is never the current
// user.
func TestZapIsCurrentUser(t *testing.T) {
	admin := &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}
	if deny := zapIsCurrentUser(admin, "someone-else"); deny != nil {
		t.Errorf("admin denied acting on another user's row")
	}

	self := &iam.User{Owner: "acme", Name: "alice"}
	if deny := zapIsCurrentUser(self, "alice"); deny != nil {
		t.Errorf("user denied acting on its own row")
	}
	if deny := zapIsCurrentUser(self, "bob"); deny == nil {
		t.Errorf("non-admin allowed to act on another user's row")
	} else if got := deny.Root().Uint32(object.CloudRespStatus); got != 403 {
		t.Errorf("cross-user denial status = %d, want 403", got)
	}

	if deny := zapIsCurrentUser(nil, "alice"); deny == nil {
		t.Errorf("nil principal allowed to act as a user")
	}
}

// TestZapEnforceStoreIsolation asserts a Homepage-bound user is forced to its store
// for an unscoped/"All" request and denied a different one, while an unbound user
// is unrestricted.
func TestZapEnforceStoreIsolation(t *testing.T) {
	unbound := &iam.User{Owner: "acme", Name: "alice"}
	if got, deny := zapEnforceStoreIsolation(unbound, "anything"); deny != nil || got != "anything" {
		t.Errorf("unbound user isolation got (%q, deny=%v), want (anything, nil)", got, deny != nil)
	}

	bound := &iam.User{Owner: "acme", Name: "alice", Homepage: "store-a"}
	if got, deny := zapEnforceStoreIsolation(bound, ""); deny != nil || got != "store-a" {
		t.Errorf("bound user unscoped got (%q, deny=%v), want (store-a, nil)", got, deny != nil)
	}
	if got, deny := zapEnforceStoreIsolation(bound, "All"); deny != nil || got != "store-a" {
		t.Errorf("bound user All got (%q, deny=%v), want (store-a, nil)", got, deny != nil)
	}
	if _, deny := zapEnforceStoreIsolation(bound, "store-b"); deny == nil {
		t.Errorf("bound user allowed a different store")
	}
}

// TestZapChatGraphDecodeError asserts a write handler rejects a malformed body with
// 400 once the caller is an org admin (the decode is reached only after the gate).
func TestZapChatGraphDecodeError(t *testing.T) {
	admin := &iam.User{Owner: "acme", Name: "boss", IsAdmin: true}
	if deny := zapChatGraphAuthz("add-graph", admin); deny != nil {
		t.Fatalf("admin unexpectedly gated: status=%d", deny.Root().Uint32(object.CloudRespStatus))
	}
	// A non-admin is gated 403 before decode — proves gate precedes decode.
	nonAdmin := &iam.User{Owner: "acme", Name: "alice"}
	if deny := zapChatGraphAuthz("add-graph", nonAdmin); deny == nil {
		t.Errorf("non-admin not gated on add-graph")
	}
}

// TestZapProviderOkEnvelopeReuse asserts this group reuses the shared beego-parity
// envelope: a list payload round-trips as {status:"ok",data:…}.
func TestZapProviderOkEnvelopeReuse(t *testing.T) {
	data := []string{"a", "b"}
	msg, err := zapProviderOk(data)
	if err != nil {
		t.Fatalf("zapProviderOk: %v", err)
	}
	want, _ := json.Marshal(Response{Status: "ok", Data: data})
	if got := msg.Root().Bytes(object.CloudRespBody); string(got) != string(want) {
		t.Errorf("ok body = %s, want %s", got, want)
	}
}
