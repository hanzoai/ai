// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// Regenerating a turn deletes the two messages it replaces. It found them with a
// chat name and an organization both read off the request body, and an empty
// organization asks every one of them — so the turn it dropped did not have to be
// the caller's.
func TestARegenerateDropsOnlyTheCallersOwnTurn(t *testing.T) {
	withStore(t)
	people := withIAM(t)

	if _, err := object.AddChat(&object.Chat{
		Owner: chatOwner, Name: "c1", Organization: "victim", User: "val",
	}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*object.Message{
		{Owner: chatOwner, Name: "m1", Organization: "victim", Chat: "c1", User: "val", Author: "val", Text: "their question"},
		{Owner: chatOwner, Name: "m2", Organization: "victim", Chat: "c1", User: "val", Author: "AI", Text: "their answer"},
	} {
		m.CreatedTime = util.GetCurrentTime()
		if _, err := object.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	mallory := people.signedIn(t, &iam.User{Owner: "acme", Name: "mallory"})
	c := as(visit("POST", "/v1/ai/add-message"), mallory)
	c.Fiber().Request().SetBody([]byte(`{"chat":"c1","user":"mallory","isRegenerated":true,"text":"hi"}`))
	c.AddMessage()

	left, err := object.GetChatMessages("c1", "victim")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 {
		t.Errorf("another organization's chat holds %d messages, want its 2 — the regenerate reached it", len(left))
	}

	// And the ordinary path still works: a turn added to a chat in the caller's own
	// organization lands, and a regenerate there drops what it replaces.
	if _, err := object.AddChat(&object.Chat{
		Owner: chatOwner, Name: "c2", Organization: "acme", User: "mallory",
	}); err != nil {
		t.Fatal(err)
	}
	c = as(visit("POST", "/v1/ai/add-message"), mallory)
	c.Fiber().Request().SetBody([]byte(`{"chat":"c2","user":"mallory","text":"mine"}`))
	c.AddMessage()
	mine, err := object.GetChatMessages("c2", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) == 0 {
		t.Errorf("a turn in the caller's own chat did not land: %s", sent(c))
	}
}

// Updating a turn authorizes on the row's own user, and that question is not asked
// of an admin at all — while an admin is an admin OF an organization, and this one
// never said which. The row's organization is what says whose turn it is.
func TestUpdatingATurnStaysInsideTheCallersOrg(t *testing.T) {
	withStore(t)
	people := withIAM(t)

	m := &object.Message{
		Owner: chatOwner, Name: "m1", Organization: "victim", Chat: "c1",
		User: "val", Author: "val", Text: "their words",
	}
	m.CreatedTime = util.GetCurrentTime()
	if _, err := object.AddMessage(m); err != nil {
		t.Fatal(err)
	}

	// An admin — of a different organization.
	mallory := people.signedIn(t, &iam.User{Owner: "acme", Name: "mallory", IsAdmin: true})
	c := as(visit("POST", "/v1/ai/update-message?id=victim/m1"), mallory)
	c.Fiber().Request().SetBody([]byte(`{"owner":"admin","name":"m1","user":"val","text":"rewritten"}`))
	c.UpdateMessage()

	after, err := object.GetMessage(util.GetIdFromOwnerAndName(chatOwner, "m1"))
	if err != nil {
		t.Fatal(err)
	}
	if after == nil {
		t.Fatal("the turn is gone")
	}
	if after.Text != "their words" {
		t.Errorf("another organization's turn now reads %q", after.Text)
	}
}

// The chat family authorizes against the STORED row's user, which is right as far
// as it goes — but that question is not asked of an admin, and an admin
// administers ONE organization. The row's organization is what says whose chat it
// is, on both surfaces.
func TestTheChatFamilyStaysInsideTheCallersOrg(t *testing.T) {
	withStore(t)
	people := withIAM(t)

	seed := func(t *testing.T) {
		t.Helper()
		if _, err := object.AddChat(&object.Chat{
			Owner: chatOwner, Name: "c1", Organization: "victim", User: "val",
			DisplayName: "their-conversation",
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed(t)

	mallory := people.signedIn(t, &iam.User{Owner: "acme", Name: "mallory", IsAdmin: true})

	// Read
	c := as(visit("GET", "/v1/ai/get-chat?id=admin/c1"), mallory)
	c.GetChat()
	if strings.Contains(sent(c), "their-conversation") {
		t.Errorf("get-chat answered another organization's chat: %s", sent(c))
	}

	// Write
	c = as(visit("POST", "/v1/ai/update-chat?id=admin/c1"), mallory)
	c.Fiber().Request().SetBody([]byte(`{"owner":"admin","name":"c1","user":"val","displayName":"taken"}`))
	c.UpdateChat()
	after, err := object.GetChat(util.GetIdFromOwnerAndName(chatOwner, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	if after == nil || after.DisplayName != "their-conversation" {
		t.Errorf("update-chat rewrote another organization's chat: %+v", after)
	}

	// Delete
	c = as(visit("POST", "/v1/ai/delete-chat"), mallory)
	c.Fiber().Request().SetBody([]byte(`{"owner":"admin","name":"c1","user":"val"}`))
	c.DeleteChat()
	still, err := object.GetChat(util.GetIdFromOwnerAndName(chatOwner, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	if still == nil {
		t.Error("delete-chat destroyed another organization's chat")
	}

	// The same three on the other surface, on a row of their own: the deletes above
	// change what is there, so sharing one row would let a later assertion pass
	// because an earlier one destroyed its subject.
	if _, err := object.AddChat(&object.Chat{
		Owner: chatOwner, Name: "c2", Organization: "victim", User: "val",
		DisplayName: "their-conversation",
	}); err != nil {
		t.Fatal(err)
	}

	msg, err := zapGetChatHandler(context.Background(), mallory, []byte(`{"id":"admin/c2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(msg.Root().Bytes(object.CloudRespBody)); strings.Contains(got, "their-conversation") {
		t.Errorf("chats.get-one answered another organization's chat: %s", got)
	}

	if _, err := zapUpdateChatHandler(context.Background(), mallory,
		[]byte(`{"id":"admin/c2","chat":{"owner":"admin","name":"c2","user":"val","displayName":"taken"}}`)); err != nil {
		t.Fatal(err)
	}
	after2, err := object.GetChat(util.GetIdFromOwnerAndName(chatOwner, "c2"))
	if err != nil {
		t.Fatal(err)
	}
	if after2 == nil || after2.DisplayName != "their-conversation" {
		t.Errorf("chats.update rewrote another organization's chat: %+v", after2)
	}

	if _, err := zapDeleteChatHandler(context.Background(), mallory,
		[]byte(`{"owner":"admin","name":"c2","user":"val"}`)); err != nil {
		t.Fatal(err)
	}
	still2, err := object.GetChat(util.GetIdFromOwnerAndName(chatOwner, "c2"))
	if err != nil {
		t.Fatal(err)
	}
	if still2 == nil {
		t.Error("chats.delete destroyed another organization's chat")
	}
}
