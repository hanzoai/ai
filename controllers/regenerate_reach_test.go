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
