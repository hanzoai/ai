// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"testing"

	"github.com/hanzoai/ai/object"
)

// A chat carries three separate names and only one of them is the tenant:
// Owner is the namespace every chat shares, User is the person, and Organization
// is the customer. A listing that leaves the organization out answers across all
// of them.
func TestAListingSeesOneTenant(t *testing.T) {
	withStore(t)
	for _, c := range []*object.Chat{
		{Owner: chatOwner, Name: "c-acme", Organization: "acme", User: "alice", Store: "s"},
		{Owner: chatOwner, Name: "c-other", Organization: "other", User: "bob", Store: "s"},
	} {
		if _, err := object.AddChat(c); err != nil {
			t.Fatalf("seed %s: %v", c.Name, err)
		}
		if _, err := object.AddMessage(&object.Message{
			Owner: chatOwner, Name: "m-" + c.Name, Organization: c.Organization,
			Store: c.Store, User: c.User, Chat: c.Name, Text: "secret of " + c.Organization,
		}); err != nil {
			t.Fatalf("seed message for %s: %v", c.Name, err)
		}
	}

	t.Run("chats", func(t *testing.T) {
		got, err := object.GetChats(chatOwner, "acme", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Organization != "acme" {
			t.Fatalf("acme sees %d chats %+v, want only its own", len(got), got)
		}
	})

	// Every caller's chats, which is the reserved org's to ask for.
	t.Run("across tenants", func(t *testing.T) {
		got, err := object.GetChats(chatOwner, "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("unconfined listing saw %d chats, want 2", len(got))
		}
	})

	// A chat name is unique across the store, not within a tenant, so the name
	// alone would hand over another customer's conversation.
	t.Run("transcript by name", func(t *testing.T) {
		got, err := object.GetChatMessages("c-other", "acme")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("acme read %d of other's messages: %+v", len(got), got)
		}
		mine, err := object.GetChatMessages("c-acme", "acme")
		if err != nil {
			t.Fatal(err)
		}
		if len(mine) != 1 {
			t.Fatalf("acme read %d of its own messages, want 1", len(mine))
		}
	})
}

// An empty filter value means unconstrained. It used to mean "equal to the empty
// string", which matched nothing — so the admin listings silently answered empty.
func TestAnEmptyFilterMeansEveryValue(t *testing.T) {
	withStore(t)
	if _, err := object.AddChat(&object.Chat{
		Owner: chatOwner, Name: "c1", Organization: "acme", User: "alice", Store: "s1",
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct{ store, user string }{{"", ""}, {"s1", ""}, {"", "alice"}} {
		got, err := object.GetChats(chatOwner, "acme", q.store, q.user)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("store=%q user=%q found %d chats, want the 1 that exists", q.store, q.user, len(got))
		}
	}
}
