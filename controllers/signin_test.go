// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// The double has to produce a credential the real verification accepts, or every
// test built on it is testing nothing.
func TestTheSignInHarnessProducesARealCredential(t *testing.T) {
	people := withIAM(t)
	credential := people.signedIn(t, &iam.User{Owner: "acme", Name: "alice", IsAdmin: true})

	c := as(visit("GET", "/v1/ai/whoami"), credential)
	user := c.GetSessionUser()
	if user == nil {
		t.Fatal("a credential the double minted was not accepted")
	}
	if user.Owner != "acme" || user.Name != "alice" || !user.IsAdmin {
		t.Errorf("it resolved to %+v", user)
	}

	// And something it did not mint is refused.
	if c := as(visit("GET", "/x"), "Bearer not-a-token"); c.GetSessionUser() != nil {
		t.Error("a credential nobody signed was accepted")
	}
	if visit("GET", "/x").GetSessionUser() != nil {
		t.Error("a request carrying nothing resolved to somebody")
	}
}
