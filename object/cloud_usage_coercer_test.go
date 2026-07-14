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

package object

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCloudUsageCoercersHandleBridgeJSON proves the cu* coercers accept the JSON
// scalar types the ZAP datastore bridge emits — decoded exactly as
// decodeNDJSON does (UseNumber). This is the coercer gate for the transport
// cut: the native driver returns native ClickHouse types (uint64/time.Time),
// the bridge returns NDJSON (json.Number for numbers, string for text/DateTime).
// If a coercer zero-defaulted on a JSON type the ledger reads would silently
// under-report; this pins that they do not.
func TestCloudUsageCoercersHandleBridgeJSON(t *testing.T) {
	// A bridge-shaped row: a UInt64 beyond float64's exact range (2^53+1), a
	// string-encoded number, a text org, a CH DateTime string, and a UInt8 flag.
	dec := json.NewDecoder(strings.NewReader(
		`{"cost_cents":9007199254740993,"total_tokens":"4096","organization":"orgA",` +
			`"timestamp":"2026-07-14 12:00:00","is_stream":1}`))
	dec.UseNumber()
	var row map[string]interface{}
	if err := dec.Decode(&row); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// json.Number keeps the exact bits; a float64 decode would round to 2^53.
	if got := cuInt64(row["cost_cents"]); got != 9007199254740993 {
		t.Fatalf("cost_cents via json.Number: got %d, want 9007199254740993 (float64 would lose precision)", got)
	}
	if got := cuInt64(row["total_tokens"]); got != 4096 {
		t.Fatalf("total_tokens via string: got %d, want 4096", got)
	}
	if got := cuString(row["organization"]); got != "orgA" {
		t.Fatalf("organization: got %q, want orgA", got)
	}
	if got := cuTime(row["timestamp"]); got.IsZero() {
		t.Fatal("timestamp: CH DateTime string not parsed")
	}
	if !cuBool(row["is_stream"]) {
		t.Fatal("is_stream=1 should coerce to true")
	}
}
