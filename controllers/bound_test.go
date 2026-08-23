// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// A user bound to one store reads that store and no other. Both doors ask this,
// and it was spelled three times before they did.
func TestWhichStoreACallerMayRead(t *testing.T) {
	bounded := &iam.User{Owner: "acme", Name: "alice", Homepage: "s1"}
	free := &iam.User{Owner: "acme", Name: "bob"}

	for _, c := range []struct {
		what      string
		user      *iam.User
		requested string
		want      string
		allowed   bool
	}{
		{"their own store", bounded, "s1", "s1", true},
		{"no store named", bounded, "", "s1", true},
		{"every store", bounded, "All", "s1", true},
		{"someone else's", bounded, "s2", "", false},
		{"unbound, a store", free, "s2", "s2", true},
		{"unbound, none", free, "", "", true},
		{"unbound, every", free, "All", "All", true},
		{"nobody at all", nil, "s2", "s2", true},
	} {
		got, ok := bound(c.user, c.requested)
		if got != c.want || ok != c.allowed {
			t.Errorf("%s: bound(%v, %q) = %q, %v; want %q, %v",
				c.what, c.user, c.requested, got, ok, c.want, c.allowed)
		}
	}
}
