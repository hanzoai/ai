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

// TestRetrievalOwnerIsThePrincipalsOrg proves the RAG tenant is the org of the
// principal the request already resolved to, and that there is no second way in.
// retrievalOwner takes no token and no Origin, so a cross-tenant spoof has nothing
// to spoof: whoever the credential resolved to is whose store is read.
func TestRetrievalOwnerIsThePrincipalsOrg(t *testing.T) {
	if got := retrievalOwner(&iam.User{Owner: "maxpower", Name: "dave"}); got != "maxpower" {
		t.Errorf("authed user owner = %q, want maxpower", got)
	}

	// A machine principal is its org just the same — this is the shape a
	// publishable key resolves to once GetOrg has named its tenant.
	if got := retrievalOwner(&iam.User{Owner: "acme", Type: iam.Machine}); got != "acme" {
		t.Errorf("machine principal owner = %q, want acme", got)
	}

	// UNRESOLVED IS UNATTRIBUTABLE. No principal names no tenant, so the caller
	// reads nothing rather than falling back to some configured default org.
	if got := retrievalOwner(nil); got != "" {
		t.Errorf("no principal => %q, want empty", got)
	}
	if got := retrievalOwner(&iam.User{}); got != "" {
		t.Errorf("principal with no org => %q, want empty", got)
	}
}
