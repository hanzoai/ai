// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// Which organization a request acts in is resolved once. Thirty-two handlers ask
// twice or more, and resolving validates the credential — for an IAM API key it
// asks IAM — so asking again costs another round trip and can answer differently
// if IAM does not reply the second time. A stated org that changes between two
// asks is the visible form of a second resolution.
func TestTheRequestsOrgIsResolvedOnce(t *testing.T) {
	withStore(t)
	people := withIAM(t)

	// A super admin, because a stated org is honoured for one — which makes a second
	// resolution observable.
	root := people.signedIn(t, &iam.User{Owner: "admin", Name: "root"})

	c := as(visit("GET", "/v1/ai/get-account"), root)
	c.Fiber().Request().Header.Set("X-Org-Id", "acme")
	if got := c.GetOrg(); got != "acme" {
		t.Fatalf("first answer = %q, want acme", got)
	}

	c.Fiber().Request().Header.Set("X-Org-Id", "other")
	if got := c.GetOrg(); got != "acme" {
		t.Errorf("second answer = %q — the request was resolved again", got)
	}

	// Until the caller changes, which is the one thing that can change the answer.
	c.SetSessionClaims(nil)
	if got := c.GetOrg(); got == "acme" {
		t.Errorf("after the caller changed, the answer is still %q", got)
	}
}

// A header value is a view of the connection's buffer, and fasthttp refills that
// buffer with the next request on the same connection — fiber's own documentation
// says it is valid only while the handler runs. An organization read from one is
// written onto rows and carried into work that outlives the handler, so what the
// resolver answers has to be a value of ours and not a window onto that buffer.
func TestAnOrgReadFromAHeaderIsOurs(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	root := people.signedIn(t, &iam.User{Owner: "admin", Name: "root"})

	c := as(visit("GET", "/v1/ai/get-account"), root)
	c.Fiber().Request().Header.Set("X-Org-Id", "acme")
	held := c.resolveOrg()

	// The buffer is refilled, which is what the next request on this connection does.
	c.Fiber().Request().Header.Set("X-Org-Id", "other")

	if held != "acme" {
		t.Errorf("the organization held from the first read now reads %q", held)
	}
}
