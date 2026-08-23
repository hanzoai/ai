// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// as presents a credential on a request the way a client does.
func as(c *ApiController, credential string) *ApiController {
	c.Fiber().Request().Header.Set("Authorization", credential)
	return c
}

// seedTwoTenants gives two customers a chat and a message each, in one store.
func seedTwoTenants(t *testing.T) {
	t.Helper()
	for _, tenant := range []struct{ org, user, chat string }{
		{"acme", "alice", "c-acme"},
		{"other", "bob", "c-other"},
	} {
		if _, err := object.AddChat(&object.Chat{
			Owner: chatOwner, Name: tenant.chat, Organization: tenant.org,
			User: tenant.user, Store: "s1", DisplayName: tenant.org + " chat",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := object.AddMessage(&object.Message{
			Owner: chatOwner, Name: "m-" + tenant.chat, Organization: tenant.org,
			User: tenant.user, Store: "s1", Chat: tenant.chat, Author: "AI",
			Text: "the secret of " + tenant.org,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// Whose chats to list arrives in the URL, which makes it a request rather than an
// answer. Without a credential there is nobody to answer it for.
func TestListingChatsNeedsACredential(t *testing.T) {
	withStore(t)
	withIAM(t)
	seedTwoTenants(t)

	c := visit("GET", "/v1/ai/get-chats?user=alice&store=s1")
	c.GetChats()
	if answered(c) != 401 {
		t.Errorf("an unsigned request naming a user answered %d: %s", answered(c), sent(c))
	}
	if strings.Contains(sent(c), "c-acme") {
		t.Errorf("it returned that user's chats: %s", sent(c))
	}
}

// And with one, it answers for the caller's own tenant.
func TestListingChatsSeesOneTenant(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	seedTwoTenants(t)

	acme := people.signedIn(t, &iam.User{Owner: "acme", Name: "alice", IsAdmin: true})
	c := as(visit("GET", "/v1/ai/get-chats?store=s1"), acme)
	c.GetChats()
	body := sent(c)
	if answered(c) != 200 {
		t.Fatalf("acme's own admin answered %d: %s", answered(c), body)
	}
	if !strings.Contains(body, "c-acme") {
		t.Errorf("acme could not see its own chat: %s", body)
	}
	if strings.Contains(body, "c-other") {
		t.Errorf("acme saw another tenant's chat: %s", body)
	}

	// Naming the other tenant's user does not reach them either.
	c = as(visit("GET", "/v1/ai/get-chats?store=s1&selectedUser=bob"), acme)
	c.GetChats()
	if strings.Contains(sent(c), "c-other") {
		t.Errorf("naming another tenant's user reached their chats: %s", sent(c))
	}
}

// A chat name is unique across the store rather than within a tenant, so asking
// for one by name is asking for whoever's it is.
func TestReadingATranscriptByName(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	seedTwoTenants(t)

	acme := people.signedIn(t, &iam.User{Owner: "acme", Name: "alice"})
	c := as(visit("GET", "/v1/ai/get-messages?chat=c-other"), acme)
	c.GetMessages()
	if strings.Contains(sent(c), "secret of other") {
		t.Errorf("acme read another tenant's transcript: %s", sent(c))
	}

	c = as(visit("GET", "/v1/ai/get-messages?chat=c-acme"), acme)
	c.GetMessages()
	if !strings.Contains(sent(c), "secret of acme") {
		t.Errorf("acme could not read its own transcript: %s", sent(c))
	}
}

// A listing across every tenant asks the platform predicate, not the tenant one
// every customer's own admin satisfies.
func TestTheCrossTenantListingsAskThePlatform(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	seedTwoTenants(t)
	if _, err := object.AddStore(&object.Store{Owner: "acme", Name: "s1"}); err != nil {
		t.Fatal(err)
	}

	tenantAdmin := people.signedIn(t, &iam.User{Owner: "acme", Name: "dave", IsAdmin: true})
	for _, call := range []struct {
		name string
		run  func(*ApiController)
		at   string
	}{
		{"GetGlobalStores", (*ApiController).GetGlobalStores, "/v1/ai/get-global-stores"},
		{"GetGlobalMessages", (*ApiController).GetGlobalMessages, "/v1/ai/get-global-messages"},
	} {
		// No credential at all.
		c := visit("GET", call.at)
		call.run(c)
		if answered(c) == 200 && !strings.Contains(sent(c), "error") {
			t.Errorf("%s answered an unsigned request: %d %s", call.name, answered(c), sent(c))
		}
		// A tenant's own admin is not the platform.
		c = as(visit("GET", call.at), tenantAdmin)
		call.run(c)
		if answered(c) == 200 && !strings.Contains(sent(c), "error") {
			t.Errorf("%s answered a tenant's own admin: %d %s", call.name, answered(c), sent(c))
		}
	}

	// The reserved org is the platform, and reaches across.
	sudo := people.signedIn(t, &iam.User{Owner: util.AdminOrg, Name: "z"})
	c := as(visit("GET", "/v1/ai/get-global-stores"), sudo)
	c.GetGlobalStores()
	if answered(c) != 200 {
		t.Errorf("the platform was refused its own listing: %d %s", answered(c), sent(c))
	}
}

// A video row is owned by whoever uploaded it, so a listing answers for that
// person — not for their organization, which is what the column does not hold.
func TestListingVideosSeesYourOwn(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	for _, v := range []*object.Video{
		{Owner: "alice", Name: "v-alice", DisplayName: "alice's"},
		{Owner: "bob", Name: "v-bob", DisplayName: "bob's"},
	} {
		if _, err := object.AddVideo(v); err != nil {
			t.Fatal(err)
		}
	}

	c := visit("GET", "/v1/ai/get-videos")
	c.GetVideos()
	if answered(c) != 401 {
		t.Errorf("an unsigned request answered %d: %s", answered(c), sent(c))
	}

	alice := people.signedIn(t, &iam.User{Owner: "acme", Name: "alice"})
	c = as(visit("GET", "/v1/ai/get-videos"), alice)
	c.GetVideos()
	body := sent(c)
	if !strings.Contains(body, "v-alice") {
		t.Errorf("alice could not see her own video: %s", body)
	}
	if strings.Contains(body, "v-bob") {
		t.Errorf("alice saw someone else's video: %s", body)
	}
}

// A store write says WHICH store to act on. What that store currently is — the
// default flag above all — is read from the row, not from the request that wants
// to change it.
func TestAStoreWriteReadsTheStoredRow(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	if _, err := object.AddStore(&object.Store{
		Owner: "acme", Name: "s1", DisplayName: "acme's store", IsDefault: true,
	}); err != nil {
		t.Fatal(err)
	}

	alice := people.signedIn(t, &iam.User{Owner: "acme", Name: "alice", IsAdmin: true})

	// A body declaring itself non-default does not walk past the guard that keeps
	// one default store in place.
	c := as(visit("POST", "/v1/ai/delete-store"), alice)
	c.Fiber().Request().SetBody([]byte(`{"owner":"acme","name":"s1","isDefault":false}`))
	c.DeleteStore()
	if stored, err := object.GetStore("acme/s1"); err != nil {
		t.Fatal(err)
	} else if stored == nil {
		t.Error("the default store was deleted by a request that said it was not default")
	}

	// And another tenant cannot name it at all.
	other := people.signedIn(t, &iam.User{Owner: "other", Name: "bob", IsAdmin: true})
	c = as(visit("POST", "/v1/ai/delete-store"), other)
	c.Fiber().Request().SetBody([]byte(`{"owner":"acme","name":"s1","isDefault":false}`))
	c.DeleteStore()
	if stored, err := object.GetStore("acme/s1"); err != nil {
		t.Fatal(err)
	} else if stored == nil {
		t.Error("another organization deleted this one's store")
	}
}
