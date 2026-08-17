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

package routers

// NO CREDENTIAL IS NO USER — never a panic, and never somebody.
//
// This used to guard a nil session store: GetSessionUser dereferenced a store that
// the embedded binary never populated, so every /v1 request took a pre-model 500.
// The store is gone and identity is the request's verified token, which changes
// where the danger is but not that there is one — the filters ask this BEFORE any
// controller, so an absent or unreadable credential has to resolve to "nobody"
// rather than fault or, worse, resolve to somebody.

import (
	"net/http"
	"testing"
)

func TestNoCredentialIsNobody(t *testing.T) {
	for _, c := range []struct{ name, authz string }{
		{"no header at all", ""},
		{"an empty bearer", "Bearer "},
		{"a bearer that is not a token", "Bearer not-a-jwt"},
		{"a bare token with no scheme", "eyJhbGciOiJSUzI1NiJ9.e30.x"},
		{"the wrong scheme", "Basic dXNlcjpwYXNz"},
		{"three dots and nothing else", "Bearer ..."},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := ask(http.MethodPost, "/v1/chat/completions")
			if c.authz != "" {
				q = q.with("Authorization", c.authz)
			}
			if u := GetSessionUser(q.Ctx); u != nil {
				t.Fatalf("resolved a user (%s/%s) from %q — an unverifiable credential must be nobody",
					u.Owner, u.Name, c.authz)
			}
		})
	}
}

// And the billing key derived from it is empty rather than a guess: the filters bill
// and rate-limit by what this returns, so a fabricated subject would charge someone.
func TestNoCredentialBillsNobody(t *testing.T) {
	q := ask(http.MethodPost, "/v1/chat/completions")
	subject, namespace, userKey := resolveBillingKey(q.Ctx)
	if subject != "" || namespace != "" || userKey != "" {
		t.Fatalf("an anonymous request resolved a billing subject (%q, %q, %q)", subject, namespace, userKey)
	}
}
