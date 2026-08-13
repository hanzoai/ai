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

package iam

import (
	"testing"

	"github.com/golang-jwt/jwt/v4"
	"github.com/hanzoai/account"
)

// TestMachineTokenIsTyped pins the claim shape this reads, because it is the whole
// reason the machine predicate was inert on the token path.
//
// The first case is the live payload, transcribed: hanzo.id's client-credentials
// token for hanzo-cloud carries no `type`, so account.IsMachine saw "" and called a
// program a person — which is how an application ended up in the user column of a
// spend row and stayed there.
func TestMachineTokenIsTyped(t *testing.T) {
	for _, c := range []struct {
		name string
		in   Claims
		want string
		why  string
	}{
		{
			name: "the live client-credentials token",
			in: Claims{
				User:             User{Owner: "hanzo", Name: "hanzo-cloud", BillingAccount: "org:hanzo"},
				RegisteredClaims: jwt.RegisteredClaims{Subject: "admin/hanzo-cloud", Audience: jwt.ClaimStrings{"hanzo-cloud"}},
			},
			want: machineType,
			why:  "the client is its own subject — that IS client_credentials",
		},
		{
			name: "a person signed into the same application",
			in: Claims{
				User:             User{Owner: "hanzo", Name: "alice"},
				RegisteredClaims: jwt.RegisteredClaims{Subject: "hanzo/alice", Audience: jwt.ClaimStrings{"hanzo-cloud"}},
			},
			want: "",
			why:  "a person is named for herself and minted for an application; they differ",
		},
		{
			name: "IAM said so itself",
			in: Claims{
				User:             User{Owner: "hanzo", Name: "svc", Type: "service-account"},
				RegisteredClaims: jwt.RegisteredClaims{Audience: jwt.ClaimStrings{"conf"}},
			},
			want: "service-account",
			why:  "an explicit type is never overwritten",
		},
		{
			name: "no audience, no answer",
			in:   Claims{User: User{Owner: "hanzo", Name: "alice"}},
			want: "",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := c.in
			got.typeMachine()
			if got.User.Type != c.want {
				t.Fatalf("Type = %q, want %q — %s", got.User.Type, c.want, c.why)
			}
		})
	}
}

// TestMachineTokenReachesThePredicate closes the loop: typing the claim is only
// useful if account.IsMachine — the one predicate money and attribution both read —
// answers differently because of it.
func TestMachineTokenReachesThePredicate(t *testing.T) {
	app := Claims{
		User:             User{Owner: "hanzo", Name: "hanzo-cloud"},
		RegisteredClaims: jwt.RegisteredClaims{Audience: jwt.ClaimStrings{"hanzo-cloud"}},
	}
	if account.IsMachine(app.User.Type) {
		t.Fatal("precondition: an untyped claim must read as a person, which was the bug")
	}
	app.typeMachine()
	if !account.IsMachine(app.User.Type) {
		t.Fatal("a client-credentials token still reads as a person; the predicate never fires")
	}
	// And the money answer is unchanged either way: an application's payer is its
	// org, so typing the credential moves attribution and never the debit.
	if got := app.User.PayerSubject("hanzo"); got != "hanzo" {
		t.Fatalf("machine pays %q, want the org pool", got)
	}
}
