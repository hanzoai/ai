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
	"encoding/json"
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
			want: Machine,
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

// TestStatedClassSurvivesTheParse pins the half of this that IAM now owns.
//
// The inference above reads the token's SHAPE, and a shape can be silent: IAM
// qualifies a SHARED app's audience with the org it was minted for, so `aud` and
// `name` stop matching and the inference reports that machine as a person — the
// one case it cannot see, and one no reader of the token could detect. IAM states
// the class outright (oidc.Claims.Type, from the grant), which is a fact rather
// than a reading of one.
//
// Decoded here as JSON, because that is the only way this arrives: the claim lands
// on the embedded User through field promotion, and a field added to the outer
// Claims would SHADOW it and leave Type empty on every token — silently, and with
// money downstream.
func TestStatedClassSurvivesTheParse(t *testing.T) {
	for _, c := range []struct {
		name    string
		payload string
		want    string
		why     string
	}{
		{
			name:    "a shared app, whose audience the inference cannot read",
			payload: `{"owner":"hanzo","name":"hanzo-cloud","type":"application","aud":"hanzo-cloud-org-hanzo"}`,
			want:    "application",
			why:     "aud != name here, so nothing but the stated class can answer",
		},
		{
			name:    "a single-tenant app, where the two agree",
			payload: `{"owner":"hanzo","name":"hanzo-cloud","type":"application","aud":"hanzo-cloud"}`,
			want:    "application",
			why:     "the stated class and the inference must not disagree",
		},
		{
			name:    "a person, who states none",
			payload: `{"owner":"acme","name":"alice","aud":"hanzo-console"}`,
			want:    "",
			why:     "only a machine has a class to state",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got Claims
			if err := json.Unmarshal([]byte(c.payload), &got); err != nil {
				t.Fatalf("decode claims: %v", err)
			}
			if got.User.Type != c.want {
				t.Fatalf("Type = %q, want %q — %s", got.User.Type, c.want, c.why)
			}
			// The parse runs the inference too; a stated class must come through it
			// unchanged, and an unstated one must not be invented for a person.
			got.typeMachine()
			if account.IsMachine(got.User.Type) != (c.want != "") {
				t.Fatalf("IsMachine(%q) = %v, want %v — %s",
					got.User.Type, account.IsMachine(got.User.Type), c.want != "", c.why)
			}
		})
	}
}
