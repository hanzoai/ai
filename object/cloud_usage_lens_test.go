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

// The two lenses over ONE usage row, proved on the read side.
//
// The property under test is not "the customer response happens not to contain a
// host today". It is that a customer read CANNOT contain one: the value is not
// selected into the process, and if it were present anyway it is not assigned.
// Each test below drives one of those two layers, and one drives the whole
// payload so the property is stated in the shape the client actually receives.

package object

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// upstreamHost is the value a customer must never receive: a real upstream host,
// distinct from the `provider` SKU on the same row.
const upstreamHost = "api.openai.com"

// adversarialRow is a warehouse row that CARRIES the upstream. Feeding it to the
// customer lens is the point: proving the read redacts a host it was handed is a
// stronger statement than proving it omits one it never had, and it is the case
// that survives someone later adding `origin` back to a shared SELECT.
func adversarialRow() map[string]interface{} {
	return map[string]interface{}{
		"timestamp":         time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC),
		"model":             "gpt-4o",
		"provider":          "hanzo",
		"status":            "success",
		"total_tokens":      int64(30),
		"prompt_tokens":     int64(10),
		"completion_tokens": int64(20),
		"cost_cents":        int64(7),
		"is_stream":         uint8(0),
		"is_premium":        uint8(0),
		"request_id":        "req-1",
		"user_id":           "maxpower/dave",
		"organization":      "maxpower",
		"origin":            upstreamHost,
	}
}

// TestCustomerLensNeverEmitsUpstream is the core invariant: across EVERY request
// shape a caller controls, the customer lens yields no upstream. The caller-facing
// params are varied exhaustively here precisely because "no matter what it asks
// for" is the claim — Admin is the one field a caller cannot set, and it is set by
// the principal, never by the request.
func TestCustomerLensNeverEmitsUpstream(t *testing.T) {
	// Everything a caller can influence, including the shapes that widen a window
	// or reach for another org. None of them is a lens.
	callerShapes := []CloudUsageParams{
		{},                                   // the DEFAULT — nobody asked for anything
		{ActivityType: "all"},                //
		{ActivityType: "inference"},          //
		{ActivityLimit: 200},                 // max page
		{ActivityOffset: 1000},               // deep page
		{TopModels: 50},                      //
		{RangeLabel: "30d"},                  //
		{Org: "admin"},                       // asking AS the reserved org name
		{Org: "maxpower", AllOrgs: true},     // asking for the god-view scope...
		{AllOrgs: true, ActivityType: "all"}, // ...without the predicate that grants it
	}

	for _, p := range callerShapes {
		if p.Admin {
			t.Fatalf("test bug: a caller shape set Admin=%v; the lens must never come from request input", p.Admin)
		}
		got := buildCloudUsageActivity(p, []map[string]interface{}{adversarialRow()}, 1)
		if len(got.Items) != 1 {
			t.Fatalf("params %+v: got %d items, want 1", p, len(got.Items))
		}
		if up := got.Items[0].Upstream; up != "" {
			t.Errorf("params %+v: customer lens leaked Upstream = %q, want \"\"", p, up)
		}
		// The customer surface is UNCHANGED — provider is still the SKU it always was.
		if pv := got.Items[0].Provider; pv != "hanzo" {
			t.Errorf("params %+v: customer provider = %q, want \"hanzo\" (the customer surface must not change)", p, pv)
		}
	}
}

// TestCustomerPayloadHasNoUpstreamKey states the property in the shape the client
// receives: not merely an empty string, but no `upstream` key in the JSON at all.
// An absent key cannot be read by a client that goes looking for one.
func TestCustomerPayloadHasNoUpstreamKey(t *testing.T) {
	overview := buildCloudUsageOverview(
		CloudUsageParams{Org: "maxpower"}, // customer: Admin is the zero value
		map[string]interface{}{}, map[string]interface{}{},
		nil, nil,
		[]map[string]interface{}{adversarialRow()}, 1,
	)
	blob, err := json.Marshal(overview)
	if err != nil {
		t.Fatalf("marshal overview: %v", err)
	}
	if strings.Contains(string(blob), "upstream") {
		t.Errorf("customer payload contains the key \"upstream\": %s", blob)
	}
	// The host itself must not appear ANYWHERE in the payload — not under this key,
	// not under another one someone adds later.
	if strings.Contains(string(blob), upstreamHost) {
		t.Errorf("customer payload contains the upstream host %q: %s", upstreamHost, blob)
	}
}

// TestAdminLensEmitsUpstream — the other half. A lens that hides from everyone is
// not a projection, it is a deletion; the admin read must actually carry the host.
func TestAdminLensEmitsUpstream(t *testing.T) {
	got := buildCloudUsageActivity(CloudUsageParams{Admin: true}, []map[string]interface{}{adversarialRow()}, 1)
	if len(got.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(got.Items))
	}
	if up := got.Items[0].Upstream; up != upstreamHost {
		t.Errorf("admin lens Upstream = %q, want %q", up, upstreamHost)
	}
	// One row, two lenses: the admin GAINS a column, and `provider` keeps meaning
	// exactly what it means for a customer. That is what keeps the two readings of
	// the same call from drifting apart.
	if pv := got.Items[0].Provider; pv != "hanzo" {
		t.Errorf("admin provider = %q, want \"hanzo\" — the admin lens adds a column, it does not rewrite one", pv)
	}
}

// TestUpstreamColumnIsLensed drives the FIRST layer directly: under the customer
// lens the column is never named in the SELECT, so the host is not in the process
// serving the response. This is the layer that holds even if the assembly is later
// rewritten.
func TestUpstreamColumnIsLensed(t *testing.T) {
	if inner, outer := cloudUsageUpstreamColumns(CloudUsageParams{}); inner != "" || outer != "" {
		t.Errorf("customer fragments = (%q, %q), want (\"\", \"\") — origin must not be selected", inner, outer)
	}
	inner, outer := cloudUsageUpstreamColumns(CloudUsageParams{Admin: true})
	if outer != ", origin" {
		t.Errorf("admin outer fragment = %q, want \", origin\"", outer)
	}
	if inner != ", any(origin) AS origin" {
		t.Errorf("admin inner fragment = %q, want \", any(origin) AS origin\"", inner)
	}
}

// TestActivitySQLProjectsOnlyAliasedColumns is the invariant that a pure-assembler
// test cannot reach, and it is not hypothetical: the first cut of this lens added
// `origin` to the OUTER select while the id-dedup subquery below it still listed
// its columns explicitly and produced no such alias. Every customer read passed —
// they never name the column — and every admin read would have failed against the
// warehouse with UNKNOWN_IDENTIFIER. The assembler tests could not see it because
// they are handed rows and never render SQL.
//
// So: whatever the outer projection names, the subquery must produce.
func TestActivitySQLProjectsOnlyAliasedColumns(t *testing.T) {
	for _, p := range []CloudUsageParams{{}, {Admin: true}} {
		inner, outer := cloudUsageUpstreamColumns(p)
		source := cloudUsageDedupedSource("1", inner)

		for _, col := range strings.Split(strings.TrimPrefix(outer, ", "), ", ") {
			if col == "" {
				continue
			}
			if !strings.Contains(source, " AS "+col) {
				t.Errorf("admin=%v: outer SELECT projects %q, but the dedup subquery aliases no such column — the query cannot run:\n%s",
					p.Admin, col, source)
			}
		}
	}
}
