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

package object

import "testing"

// NormalizeRequestId is the ONE normalization the family-event write and the
// /v1/feedback join share, so both key on the same value for either input form.
func TestNormalizeRequestId(t *testing.T) {
	cases := map[string]string{
		"chatcmpl-abc123": "abc123",    // response id form
		"  chatcmpl-x  ":  "x",         // trimmed
		"abc123":          "abc123",    // raw ledger form
		"msg_01xyz":       "msg_01xyz", // anthropic id (no chatcmpl prefix)
		"":                "",          // empty stays empty
	}
	for in, want := range cases {
		if got := NormalizeRequestId(in); got != want {
			t.Errorf("NormalizeRequestId(%q)=%q, want %q", in, got, want)
		}
	}
}

// RecordFamilyRouting with a nil DB (no adapter) is a safe no-op — the shared writer
// never panics off the hot path, and it falls back the routed model + join key.
func TestRecordFamilyRoutingNoAdapterIsSafe(t *testing.T) {
	// No adapter configured in the unit env → AddRoutingEvent is a no-op; the helper
	// must still run its field resolution without panicking (served falls back to SKU,
	// join id falls back to a generated uuid) and skip the shadow (no endpoint).
	RecordFamilyRouting(FamilyRoutingInput{
		Owner: "acme", User: "acme/bob",
		RequestedModel: "ZEN5", RoutedModel: "", ResponseId: "chatcmpl-abc",
		PromptTokens: 10, CompletionTokens: 20, CostCents: 3, LatencyMs: 42,
		RouterEndpoint: "", ShadowText: "hi", // endpoint empty → no shadow call
	})
	// A second call with an empty ResponseId exercises the uuid fallback path.
	RecordFamilyRouting(FamilyRoutingInput{
		Owner: "acme", RequestedModel: "enso", ResponseId: "",
	})
}
