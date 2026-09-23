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
)

// TestHomeOrgNamesTheAccount decodes the claim shapes hanzo.id mints and asserts
// the owner and the payer they resolve to. The first case is a live hanzo-app
// token, transcribed: owner is the app's org, the account lives in its own org,
// and the signed billing_account names that org.
func TestHomeOrgNamesTheAccount(t *testing.T) {
	for _, c := range []struct {
		name, raw, owner, payer string
	}{
		{
			name:  "an account in its own org, signed in through hanzo-app",
			raw:   `{"owner":"hanzo","organization":"hanzo","name":"joshuafl369","billing_account":"org:joshuafl369","aud":["hanzo-app"],"orgs":[{"org":"joshuafl369","role":"admin"},{"org":"guardforce","role":"admin"},{"org":"webby-ai","role":"owner"}]}`,
			owner: "joshuafl369",
			payer: "joshuafl369",
		},
		{
			name:  "a plain member of its own org",
			raw:   `{"owner":"hanzo","name":"probe","aud":["hanzo-app"],"orgs":[{"org":"acme","role":"member"}]}`,
			owner: "acme",
			payer: "acme",
		},
		{
			name:  "a member of the signup org keeps a personal wallet",
			raw:   `{"owner":"hanzo","name":"alice","aud":["hanzo-app"],"orgs":[{"org":"hanzo","role":"member"}]}`,
			owner: "hanzo",
			payer: "hanzo/alice",
		},
		{
			name:  "an admin-org operator stays in the admin org",
			raw:   `{"owner":"admin","name":"z","billing_account":"org:admin","aud":["admin-console"],"orgs":[{"org":"admin","role":"admin"},{"org":"hanzo","role":"admin"}]}`,
			owner: "admin",
			payer: "admin",
		},
		{
			name:  "a machine token has no membership set and keeps its app org",
			raw:   `{"owner":"hanzo","name":"hanzo-cloud","billing_account":"org:hanzo","aud":["hanzo-cloud"]}`,
			owner: "hanzo",
			payer: "hanzo",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got Claims
			if err := json.Unmarshal([]byte(c.raw), &got); err != nil {
				t.Fatal(err)
			}
			got.typeMachine()
			got.homeOrg()
			if got.User.Owner != c.owner {
				t.Fatalf("Owner = %q, want %q", got.User.Owner, c.owner)
			}
			if p := got.User.PayerSubject(""); p != c.payer {
				t.Fatalf("PayerSubject = %q, want %q", p, c.payer)
			}
		})
	}
}
