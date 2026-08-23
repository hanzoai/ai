// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"encoding/json"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
)

// A store is added, listed, read, renamed and removed through its handlers.
func TestAStoreLivesAndDiesThroughItsHandlers(t *testing.T) {
	withStore(t)
	iamd := withIAM(t)
	auth := iamd.asUser(t, &iam.User{Owner: "acme", Name: "alice"})

	_, env := call(t, "stores.add", auth, `{"name":"s1","displayName":"First store"}`)
	env.ok(t, "add")

	_, env = call(t, "stores.get", auth, `{}`)
	env.ok(t, "list")
	var stores []object.Store
	if err := json.Unmarshal(env.Data, &stores); err != nil {
		t.Fatalf("list data: %v (%s)", err, env.Data)
	}
	if len(stores) != 1 || stores[0].Name != "s1" {
		t.Fatalf("list returned %d stores (%+v), want the one just added", len(stores), stores)
	}
	// The row was filed into the caller's own org, which is the org the listing reads.
	if stores[0].Owner != "acme" {
		t.Errorf("store owner = %q, want the caller's own org", stores[0].Owner)
	}

	_, env = call(t, "stores.get-one", auth, `{"id":"acme/s1"}`)
	env.ok(t, "get")

	_, env = call(t, "stores.update", auth,
		`{"id":"acme/s1","owner":"acme","name":"s1","displayName":"Renamed store"}`)
	env.ok(t, "update")
	_, env = call(t, "stores.get-one", auth, `{"id":"acme/s1"}`)
	env.ok(t, "get after update")
	var got object.Store
	_ = json.Unmarshal(env.Data, &got)
	if got.DisplayName != "Renamed store" {
		t.Errorf("displayName after update = %q, want %q", got.DisplayName, "Renamed store")
	}

	_, env = call(t, "stores.delete", auth, `{"owner":"acme","name":"s1"}`)
	env.ok(t, "delete")
	_, env = call(t, "stores.get", auth, `{}`)
	env.ok(t, "list after delete")
	stores = nil
	_ = json.Unmarshal(env.Data, &stores)
	if len(stores) != 0 {
		t.Errorf("after delete the listing still holds %d: %+v", len(stores), stores)
	}
}

// A store is filed into the caller's OWN org whatever the request body says, and
// only the admin org may name a different one.
//
// The owner arrives on the body, and a default store names the model every chat
// answer runs and bills — so a member of any org being able to file one into
// another org's namespace is the whole of the concern. Asserted from both sides:
// the row does not appear where it was aimed, and it does appear where the caller
// actually lives.
func TestAStoreIsFiledIntoTheCallersOwnOrg(t *testing.T) {
	withStore(t)
	iamd := withIAM(t)

	// Alice is not in the admin org, and aims a store at somebody else's.
	alice := iamd.asUser(t, &iam.User{Owner: "acme", Name: "alice"})
	_, env := call(t, "stores.add", alice, `{"owner":"victim","name":"s1","displayName":"aimed elsewhere"}`)
	env.ok(t, "add")

	// It landed in hers.
	_, env = call(t, "stores.get", alice, `{}`)
	env.ok(t, "list as alice")
	var mine []object.Store
	_ = json.Unmarshal(env.Data, &mine)
	if len(mine) != 1 || mine[0].Owner != "acme" {
		t.Fatalf("alice sees %+v, want one store owned by acme", mine)
	}

	// And nowhere near the org she aimed it at.
	victim := iamd.asUser(t, &iam.User{Owner: "victim", Name: "bob"})
	_, env = call(t, "stores.get", victim, `{}`)
	env.ok(t, "list as victim")
	var theirs []object.Store
	_ = json.Unmarshal(env.Data, &theirs)
	if len(theirs) != 0 {
		t.Errorf("the victim org holds %d stores it never created: %+v", len(theirs), theirs)
	}

	// The admin org is the one that may name an owner, and it is the seam that
	// says so rather than the row.
	root := iamd.asUser(t, &iam.User{Owner: "admin", Name: "root", IsAdmin: true})
	_, env = call(t, "stores.add", root, `{"owner":"victim","name":"s2","displayName":"placed deliberately"}`)
	env.ok(t, "add as admin")
	_, env = call(t, "stores.get", victim, `{}`)
	env.ok(t, "list as victim after admin add")
	theirs = nil
	_ = json.Unmarshal(env.Data, &theirs)
	if len(theirs) != 1 || theirs[0].Name != "s2" {
		t.Errorf("admin could not place a store deliberately: %+v", theirs)
	}
}

// No credential, no store — the tenancy seam refuses before anything is read.
func TestStoreReadsRefuseAnAnonymousCaller(t *testing.T) {
	withStore(t)
	for _, method := range []string{"stores.get", "stores.add"} {
		t.Run(method, func(t *testing.T) {
			status, _ := call(t, method, "", `{}`)
			if status != 401 {
				t.Errorf("%s answered %d with no credential, want 401", method, status)
			}
		})
	}
}
