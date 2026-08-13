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

package controllers

import (
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// TestUsageRowMatchesSchema pins the row to the column list it claims to follow.
//
// Adding a column in the middle of object.CloudUsageColumns without adding its
// value here shifts every value after it one field left: the model lands in the
// provider column, the provider in origin, and so on to the end of the row. It
// compiles, it runs, it writes rows, and every one of them is wrong in a way that
// looks like data. This is the cheapest possible check for that, and it is the only
// thing standing between the two lists.
func TestUsageRowMatchesSchema(t *testing.T) {
	got := len(cloudUsageValues(&usageRecord{}, time.Now()))
	if want := len(object.CloudUsageColumns); got != want {
		t.Fatalf("the writer supplies %d values for %d columns — every value after "+
			"the mismatch lands in the wrong column", got, want)
	}
}

// TestUsageRowCarriesAttribution: the four columns the ledger gained exist to
// answer "who spent this, through what, on whose key, in which conversation". A row
// that drops them on the floor answers none of it while looking complete.
func TestUsageRowCarriesAttribution(t *testing.T) {
	rec := &usageRecord{
		Owner: "acme", User: "acme/alice", Agent: "hanzo/hanzo-cloud",
		Provider: "hanzo", Origin: "openrouter.ai",
		APIKeyHash: "sha256ref", Session: "sess-1", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		Model: "zen5", Status: "success",
	}
	row := cloudUsageValues(rec, time.Now())

	// Index by name off the ONE column list, so this test cannot drift from the row.
	at := map[string]any{}
	for i, c := range object.CloudUsageColumns {
		if i < len(row) {
			at[c] = row[i]
		}
	}

	for _, c := range []struct{ column, want string }{
		{"user_id", "acme/alice"},
		{"agent", "hanzo/hanzo-cloud"},
		{"provider", "hanzo"},
		{"origin", "openrouter.ai"},
		{"api_key_hash", "sha256ref"},
		{"session_id", "sess-1"},
		{"trace_id", "4bf92f3577b34da6a3ce929d0e0e4736"},
	} {
		if got, _ := at[c.column].(string); got != c.want {
			t.Errorf("%s = %q, want %q", c.column, got, c.want)
		}
	}

	// provider and origin are deliberately two columns, not one. The label is what
	// we sell; the host is where the bytes went. Keeping both is what lets a row be
	// asked whether they still agree — a customer sees "hanzo", and we can still see
	// who actually served it.
	if at["provider"] == at["origin"] {
		t.Error("provider and origin collapsed to one value; the disagreement between " +
			"them is the measurement")
	}
}
