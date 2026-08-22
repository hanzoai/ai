// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"encoding/json"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
)

// call drives one registered cloud method and returns its status and envelope.
func call(t *testing.T, method, auth, body string) (uint32, envelope) {
	t.Helper()
	h, ok := lookupCloudHandler(method)
	if !ok {
		t.Fatalf("method %q is not registered", method)
	}
	msg, err := h(context.Background(), auth, []byte(body))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var env envelope
	raw := msg.Root().Bytes(object.CloudRespBody)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("%s: body is not an envelope: %s", method, raw)
		}
	}
	return msg.Root().Uint32(object.CloudRespStatus), env
}

// envelope is the {status, msg, data} the cloud handlers answer in.
type envelope struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
}

func (e envelope) ok(t *testing.T, what string) {
	t.Helper()
	if e.Status != "ok" {
		t.Fatalf("%s: %s (%s)", what, e.Status, e.Msg)
	}
}

// A chat is added, listed, read, renamed and removed — through the handlers, and
// against a store that actually keeps it.
//
// Each step is asserted on what the NEXT read returns rather than on what the
// write answered, because a write that reports success and stores nothing answers
// exactly the same as one that works. The list going from one row back to none is
// what makes the delete a delete.
func TestAChatLivesAndDiesThroughItsHandlers(t *testing.T) {
	withStore(t)
	seedDefaultStore(t)
	auth := asUser(t, &iam.User{Owner: "acme", Name: "alice"})

	status, env := call(t, "chats.add", auth, `{"name":"c1","user":"alice","displayName":"First","store":"default"}`)
	if status != 200 {
		t.Fatalf("add: status %d", status)
	}
	env.ok(t, "add")

	// It is there, and it is the one that was added.
	_, env = call(t, "chats.list", auth, `{"user":"alice","store":"default"}`)
	env.ok(t, "list")
	var chats []object.Chat
	if err := json.Unmarshal(env.Data, &chats); err != nil {
		t.Fatalf("list data: %v (%s)", err, env.Data)
	}
	if len(chats) != 1 || chats[0].Name != "c1" {
		t.Fatalf("list returned %d chats (%+v), want the one just added", len(chats), chats)
	}
	if chats[0].DisplayName != "First" {
		t.Errorf("displayName = %q, want %q", chats[0].DisplayName, "First")
	}

	// Read back by its own id.
	_, env = call(t, "chats.get", auth, `{"id":"`+chatOwner+`/c1"}`)
	env.ok(t, "get")
	var got object.Chat
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("get data: %v (%s)", err, env.Data)
	}
	if got.Name != "c1" {
		t.Errorf("get returned %q, want c1", got.Name)
	}

	// Rename it, and read the new name back rather than trusting the write.
	_, env = call(t, "chats.update", auth,
		`{"id":"`+chatOwner+`/c1","chat":{"name":"c1","user":"alice","displayName":"Renamed"}}`)
	env.ok(t, "update")
	_, env = call(t, "chats.get", auth, `{"id":"`+chatOwner+`/c1"}`)
	env.ok(t, "get after update")
	_ = json.Unmarshal(env.Data, &got)
	if got.DisplayName != "Renamed" {
		t.Errorf("displayName after update = %q, want %q", got.DisplayName, "Renamed")
	}

	// And gone means the listing is empty, not that delete said so.
	_, env = call(t, "chats.delete", auth, `{"name":"c1","user":"alice"}`)
	env.ok(t, "delete")
	_, env = call(t, "chats.list", auth, `{"user":"alice","store":"default"}`)
	env.ok(t, "list after delete")
	chats = nil
	_ = json.Unmarshal(env.Data, &chats)
	if len(chats) != 0 {
		t.Errorf("after delete the listing still holds %d: %+v", len(chats), chats)
	}
}

// One user's chat is not another's to read, write or remove — and the store is
// real here, so the row genuinely exists while the refusal happens.
func TestOneUsersChatIsNotAnothersToTouch(t *testing.T) {
	withStore(t)
	seedDefaultStore(t)
	alice := asUser(t, &iam.User{Owner: "acme", Name: "alice"})
	if status, env := call(t, "chats.add", alice, `{"name":"c1","user":"alice","displayName":"Alice's","store":"default"}`); status != 200 || env.Status != "ok" {
		t.Fatalf("seed: status=%d %s", status, env.Msg)
	}

	bob := asUser(t, &iam.User{Owner: "acme", Name: "bob"})
	for _, tc := range []struct{ what, method, body string }{
		{"add as someone else", "chats.add", `{"name":"c2","user":"alice"}`},
		{"update someone else's", "chats.update", `{"id":"` + chatOwner + `/c1","chat":{"name":"c1","user":"alice"}}`},
		{"delete someone else's", "chats.delete", `{"name":"c1","user":"alice"}`},
	} {
		t.Run(tc.what, func(t *testing.T) {
			status, _ := call(t, tc.method, bob, tc.body)
			if status != 403 {
				t.Errorf("%s answered %d, want 403", tc.method, status)
			}
		})
	}

	// And Alice's chat survived every one of them.
	_, env := call(t, "chats.list", alice, `{"user":"alice","store":"default"}`)
	var chats []object.Chat
	_ = json.Unmarshal(env.Data, &chats)
	if len(chats) != 1 {
		t.Errorf("Alice holds %d chats after Bob's attempts, want 1", len(chats))
	}
}
