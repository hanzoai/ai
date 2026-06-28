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

	iam "github.com/hanzoai/iam"
)

// TestRetrievalGate locks when optional RAG augmentation activates: it is OFF by
// default (so a normal completion never silently fans out to the vector store)
// and ON only via the explicit X-Retrieval / X-Retrieval-Store opt-in.
func TestRetrievalGate(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"off by default", nil, false},
		{"X-Retrieval=1", map[string]string{"X-Retrieval": "1"}, true},
		{"X-Retrieval=true", map[string]string{"X-Retrieval": "true"}, true},
		{"X-Retrieval=0", map[string]string{"X-Retrieval": "0"}, false},
		{"X-Retrieval=false", map[string]string{"X-Retrieval": "false"}, false},
		{"X-Retrieval-Store set", map[string]string{"X-Retrieval-Store": "docs"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newControllerWithUser(&iam.User{Owner: "acme", Name: "a"}, tc.headers)
			if got := c.retrievalEnabled(""); got != tc.want {
				t.Fatalf("retrievalEnabled(headers=%v)=%v want %v", tc.headers, got, tc.want)
			}
		})
	}
}

// TestRetrievalDisabledReturnsEmpty asserts the default path returns no
// knowledge and makes no vector-store call — RAG must never fire unless opted in.
func TestRetrievalDisabledReturnsEmpty(t *testing.T) {
	c, _ := newControllerWithUser(&iam.User{Owner: "acme", Name: "a"}, nil)
	if got := c.retrieveKnowledgeIfEnabled("what is hanzo?", "acme", "", "en"); len(got) != 0 {
		t.Fatalf("disabled retrieval returned %d docs, want 0", len(got))
	}
}

// TestRetrievalEnabledNoOwnerReturnsEmpty asserts the owner guard: even with
// retrieval opted in, a missing owner short-circuits to empty (no cross-tenant
// or unscoped search) and never reaches the vector store.
func TestRetrievalEnabledNoOwnerReturnsEmpty(t *testing.T) {
	c, _ := newControllerWithUser(nil, map[string]string{"X-Retrieval": "1"})
	if got := c.retrieveKnowledgeIfEnabled("q", "", "", "en"); len(got) != 0 {
		t.Fatalf("retrieval with empty owner returned %d docs, want 0 (must be tenant-scoped)", len(got))
	}
}
