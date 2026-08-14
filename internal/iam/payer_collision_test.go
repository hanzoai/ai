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

	"github.com/hanzoai/account"
)

// The machine inference reads a SHAPE — the principal's name equals its audience —
// and the shape is reachable by a person, because the three things it assumed were
// disjoint are not:
//
//   - IAM's audience is the app's client id (oidc audienceFor),
//   - IAM's client ids ARE its app names: 95 of 96 registered applications have
//     clientId == name, e.g. "hanzo-cloud",
//   - IAM usernames are ^[a-z0-9][a-z0-9._-]{0,62}$ with NO reserved list, so
//     "hanzo-cloud" is an available username.
//
// So a self-serve signup can choose a username equal to the audience its own
// tokens carry. In the signup org that is not a cosmetic mistype: account.Payer's
// machine branch pays from the ORG, its person branch pays from a wallet of the
// person's own, and only the signup org has both.
//
// These decode real token payloads rather than building Claims by hand, because
// the claim lands on the embedded User by field promotion and a hand-built value
// would not exercise that.

func parseClaims(t *testing.T, payload string) Claims {
	t.Helper()
	var c Claims
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	return c
}

// TestNameAudienceCollisionDoesNotBillThePool is the takeover, pinned shut.
//
// The payload is exactly what hanzo.id mints for a plain member of the signup org
// who picked the colliding username: no `type` (IAM states a class only for
// client_credentials), no `billing_account` (store.BillingAccount returns "" for a
// non-admin member), and the membership set every person carries.
func TestNameAudienceCollisionDoesNotBillThePool(t *testing.T) {
	got := parseClaims(t, `{
		"owner":"hanzo",
		"name":"hanzo-cloud",
		"preferred_username":"hanzo-cloud",
		"aud":"hanzo-cloud",
		"orgs":[{"org":"hanzo","role":"member"}]
	}`)

	// Precondition: this really is the collision, not a payload that misses it.
	if len(got.Audience) != 1 || got.Audience[0] != got.User.Name {
		t.Fatalf("precondition: payload is not a name/audience collision (aud=%v name=%q)",
			got.Audience, got.User.Name)
	}

	got.typeMachine()

	if account.IsMachine(got.User.Type) {
		t.Fatalf("a person with a colliding username typed as a machine (Type=%q)", got.User.Type)
	}
	// The money answer is the one that matters: their own wallet, not the pool.
	if subject := got.User.PayerSubject("hanzo"); subject != "hanzo/hanzo-cloud" {
		t.Fatalf("payer subject = %q, want %q — a colliding username must not reach the platform pool",
			subject, "hanzo/hanzo-cloud")
	}
}

// TestMembershipsOutrankTheShape states the rule on its own, away from the
// signup org, so a regression is legible as "memberships lost their authority"
// rather than as an account-subject mismatch.
func TestMembershipsOutrankTheShape(t *testing.T) {
	for _, c := range []struct {
		name    string
		payload string
		machine bool
		why     string
	}{
		{
			name:    "a person whose name collides with the audience",
			payload: `{"owner":"hanzo","name":"hanzo-cloud","aud":"hanzo-cloud","orgs":[{"org":"hanzo","role":"member"}]}`,
			machine: false,
			why:     "IAM signed a membership set; only a person has one",
		},
		{
			name:    "a real client-credentials token, which states no memberships",
			payload: `{"owner":"hanzo","name":"hanzo-cloud","aud":"hanzo-cloud"}`,
			machine: true,
			why:     "the inference must still answer for the tokens it exists for",
		},
		{
			name:    "an explicit machine class is honored even with memberships",
			payload: `{"owner":"hanzo","name":"svc","type":"service-account","aud":"conf","orgs":[{"org":"hanzo","role":"member"}]}`,
			machine: true,
			why:     "a stated class is a fact; only the inference defers",
		},
		{
			name:    "a person who does not collide is unaffected",
			payload: `{"owner":"hanzo","name":"alice","aud":"hanzo-cloud","orgs":[{"org":"hanzo","role":"member"}]}`,
			machine: false,
			why:     "the ordinary case must not move",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := parseClaims(t, c.payload)
			got.typeMachine()
			if account.IsMachine(got.User.Type) != c.machine {
				t.Fatalf("IsMachine(%q) = %v, want %v — %s",
					got.User.Type, account.IsMachine(got.User.Type), c.machine, c.why)
			}
		})
	}
}

// TestSignupOrgPersonKeepsTheirOwnWallet is the property the whole fix defends,
// stated as money rather than as a type: inside the signup org, strangers share
// one org and must not share one wallet.
func TestSignupOrgPersonKeepsTheirOwnWallet(t *testing.T) {
	for _, name := range []string{"alice", "hanzo-cloud", "hanzo-console", "hanzo-ai"} {
		t.Run(name, func(t *testing.T) {
			got := parseClaims(t, `{"owner":"hanzo","name":"`+name+`","aud":"`+name+`","orgs":[{"org":"hanzo","role":"member"}]}`)
			got.typeMachine()
			if subject := got.User.PayerSubject("hanzo"); subject != "hanzo/"+name {
				t.Fatalf("payer subject = %q, want %q", subject, "hanzo/"+name)
			}
		})
	}
}
