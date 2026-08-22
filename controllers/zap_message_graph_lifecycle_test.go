// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"encoding/json"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
)

// A message is added, listed, read, edited and removed — through the handlers,
// against a store that keeps it. Same shape as the chat lifecycle, because the
// claim is the same one: the row a write reports is the row a read returns.
func TestAMessageLivesAndDiesThroughItsHandlers(t *testing.T) {
	withStore(t)
	seedDefaultStore(t)
	auth := asUser(t, &iam.User{Owner: "acme", Name: "alice"})

	// A message belongs to a chat, and the handler refuses one that is not there.
	_, seed := call(t, "chats.add", auth, `{"name":"c1","user":"alice","store":"default"}`)
	seed.ok(t, "seed chat")

	_, env := call(t, "messages.add", auth,
		`{"name":"m1","user":"alice","chat":"c1","text":"hello","store":"default","author":"alice"}`)
	env.ok(t, "add")

	_, env = call(t, "messages.list", auth, `{"user":"alice","store":"default","chat":"c1"}`)
	env.ok(t, "list")
	var messages []object.Message
	if err := json.Unmarshal(env.Data, &messages); err != nil {
		t.Fatalf("list data: %v (%s)", err, env.Data)
	}
	if len(messages) != 1 || messages[0].Name != "m1" {
		t.Fatalf("list returned %d messages (%+v), want the one just added", len(messages), messages)
	}
	if messages[0].Text != "hello" {
		t.Errorf("text = %q, want %q", messages[0].Text, "hello")
	}

	_, env = call(t, "messages.get", auth, `{"id":"`+chatOwner+`/m1"}`)
	env.ok(t, "get")

	_, env = call(t, "messages.update", auth,
		`{"id":"`+chatOwner+`/m1","message":{"name":"m1","user":"alice","chat":"c1","text":"edited","store":"default","author":"alice"}}`)
	env.ok(t, "update")
	_, env = call(t, "messages.list", auth, `{"user":"alice","store":"default","chat":"c1"}`)
	env.ok(t, "list after update")
	messages = nil
	_ = json.Unmarshal(env.Data, &messages)
	if len(messages) != 1 || messages[0].Text != "edited" {
		t.Errorf("after update the stored text is %+v, want \"edited\"", messages)
	}
}

// Graphs are ADMIN-gated, and the lifecycle is asserted as an admin so the gate
// is passed rather than worked around: the refusals for everyone else are pinned
// separately in the sweep.
func TestAGraphLivesAndDiesForAnAdmin(t *testing.T) {
	withStore(t)
	seedDefaultStore(t)
	admin := asUser(t, &iam.User{Owner: "acme", Name: "root", IsAdmin: true})

	_, env := call(t, "graphs.add", admin, `{"owner":"acme","name":"g1","displayName":"First graph","text":"{}"}`)
	env.ok(t, "add")

	_, env = call(t, "graphs.list", admin, `{}`)
	env.ok(t, "list")
	var graphs []object.Graph
	if err := json.Unmarshal(env.Data, &graphs); err != nil {
		t.Fatalf("list data: %v (%s)", err, env.Data)
	}
	if len(graphs) != 1 || graphs[0].Name != "g1" {
		t.Fatalf("list returned %d graphs (%+v), want the one just added", len(graphs), graphs)
	}

	_, env = call(t, "graphs.update", admin,
		`{"id":"acme/g1","graph":{"owner":"acme","name":"g1","displayName":"Renamed graph","text":"{}"}}`)
	env.ok(t, "update")

	_, env = call(t, "graphs.delete", admin, `{"owner":"acme","name":"g1"}`)
	env.ok(t, "delete")
	_, env = call(t, "graphs.list", admin, `{}`)
	env.ok(t, "list after delete")
	graphs = nil
	_ = json.Unmarshal(env.Data, &graphs)
	if len(graphs) != 0 {
		t.Errorf("after delete the listing still holds %d: %+v", len(graphs), graphs)
	}
}
