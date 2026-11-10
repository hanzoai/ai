// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func listedIDs(t *testing.T, body string) []string {
	t.Helper()
	var out modelList
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	var ids []string
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

// TestListModels_NamedIsTheListingNarrowed: ?id= answers the rows a page names, in
// the listing's own order, and nothing it does not list. Without it the answer is
// the whole public body, byte for byte — the listing every OpenAI-compatible
// caller reads.
func TestListModels_NamedIsTheListingNarrowed(t *testing.T) {
	withCatalog(t, "probe-a", "probe-b", "probe-c")

	whole := visit(http.MethodGet, "/v1/models")
	whole.ListModels()
	public, err := modelListing(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if answered(whole) != http.StatusOK || sent(whole) != string(public) {
		t.Fatalf("GET /v1/models = %d, want the public body unchanged", answered(whole))
	}

	for query, want := range map[string][]string{
		"id=probe-c,probe-a,never-listed": {"probe-a", "probe-c"},
		"id=probe-b":                      {"probe-b"},
		"id=+probe-b+,,":                  {"probe-b"},
		"id=never-listed":                 nil,
	} {
		c := visit(http.MethodGet, "/v1/models?"+query)
		c.ListModels()
		if answered(c) != http.StatusOK {
			t.Fatalf("?%s = %d %s", query, answered(c), sent(c))
		}
		if got := listedIDs(t, sent(c)); !slices.Equal(got, want) {
			t.Errorf("?%s = %v, want %v", query, got, want)
		}
		if !strings.Contains(sent(c), `"object":"list"`) || !strings.Contains(sent(c), `"models":[]`) {
			t.Errorf("?%s lost the list envelope: %s", query, sent(c))
		}
	}

	many := make([]string, namedMost+1)
	for i := range many {
		many[i] = "probe-a"
	}
	c := visit(http.MethodGet, "/v1/models?id="+strings.Join(many, ","))
	c.ListModels()
	if answered(c) != http.StatusBadRequest {
		t.Errorf("%d ids = %d, want 400", len(many), answered(c))
	}
	c = visit(http.MethodGet, "/v1/models?id="+strings.Join(many[:namedMost], ","))
	c.ListModels()
	if answered(c) != http.StatusOK {
		t.Errorf("%d ids = %d, want 200", namedMost, answered(c))
	}
}
