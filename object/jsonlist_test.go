// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hanzoai/ai/model"
)

// Compile-time proof that every Message slice-of-struct column satisfies the
// exact interfaces dbx/database/sql route through.
var (
	_ driver.Valuer = JSONList[VectorScore](nil)
	_ driver.Valuer = JSONList[Suggestion](nil)
	_ driver.Valuer = JSONList[model.ToolCall](nil)
	_ driver.Valuer = JSONList[model.SearchResult](nil)

	_ sql.Scanner = (*JSONList[VectorScore])(nil)
	_ sql.Scanner = (*JSONList[Suggestion])(nil)
	_ sql.Scanner = (*JSONList[model.ToolCall])(nil)
	_ sql.Scanner = (*JSONList[model.SearchResult])(nil)
)

// TestJSONListConverterFixesRootCause reproduces the P0 at the precise layer
// that failed — database/sql's DefaultParameterConverter — and proves the fix.
// A bare []struct is rejected by TYPE (so even an empty slice kills the insert);
// the JSONList wrapper is accepted because it implements driver.Valuer.
func TestJSONListConverterFixesRootCause(t *testing.T) {
	// The bug: the converter rejects a bare slice-of-struct outright.
	if _, err := driver.DefaultParameterConverter.ConvertValue([]model.SearchResult{}); err == nil {
		t.Fatal("expected database/sql to reject a bare []model.SearchResult (the P0); got nil error")
	}

	// The fix: the wrapper is converted to JSON text, empty included.
	v, err := driver.DefaultParameterConverter.ConvertValue(JSONList[model.SearchResult]{})
	if err != nil {
		t.Fatalf("JSONList[model.SearchResult] must convert cleanly, got: %v", err)
	}
	if v != "[]" {
		t.Fatalf("empty JSONList must persist as %q, got %q", "[]", v)
	}
}

func TestJSONListVectorScoreRoundTrip(t *testing.T) {
	in := JSONList[VectorScore]{{Vector: "v1", Score: 0.5}, {Vector: "v2", Score: 0.25}}
	assertRoundTrip(t, in)
}

func TestJSONListSuggestionRoundTrip(t *testing.T) {
	in := JSONList[Suggestion]{{Text: "try this", IsHit: true}, {Text: "or this", IsHit: false}}
	assertRoundTrip(t, in)
}

func TestJSONListToolCallRoundTrip(t *testing.T) {
	in := JSONList[model.ToolCall]{{Name: "search", Arguments: `{"q":"x"}`, Content: "result"}}
	assertRoundTrip(t, in)
}

func TestJSONListSearchResultRoundTrip(t *testing.T) {
	in := JSONList[model.SearchResult]{{Index: 0, Title: "T", URL: "https://example.com", SiteName: "Example"}}
	assertRoundTrip(t, in)
}

// assertRoundTrip stores via Value(), reloads via Scan(), and requires the
// result to equal the input — the full dbx persistence contract.
func assertRoundTrip[T any](t *testing.T, in JSONList[T]) {
	t.Helper()
	stored, err := in.Value()
	if err != nil {
		t.Fatalf("Value() error: %v", err)
	}
	var out JSONList[T]
	if err := out.Scan(stored); err != nil {
		t.Fatalf("Scan() error: %v", err)
	}
	if !reflect.DeepEqual([]T(in), []T(out)) {
		t.Fatalf("round-trip mismatch:\n in=%#v\nout=%#v", in, out)
	}
}

// TestJSONListNullEmpty pins NULL/empty semantics to match StringList: empty →
// "[]" on write; NULL, "" and "[]" all load as an empty list (no error).
func TestJSONListNullEmpty(t *testing.T) {
	var nilList JSONList[model.SearchResult]
	v, err := nilList.Value()
	if err != nil || v != "[]" {
		t.Fatalf("nil JSONList: want (\"[]\", nil), got (%q, %v)", v, err)
	}

	for _, src := range []interface{}{nil, "", []byte(""), "[]", []byte("[]")} {
		var out JSONList[model.SearchResult]
		if err := out.Scan(src); err != nil {
			t.Fatalf("Scan(%#v) error: %v", src, err)
		}
		if len(out) != 0 {
			t.Fatalf("Scan(%#v): want empty, got %#v", src, out)
		}
	}
}

// TestJSONListWireFormatUnchanged guarantees the API/frontend contract: a
// JSONList marshals identically to the bare slice it replaces.
func TestJSONListWireFormatUnchanged(t *testing.T) {
	bare := []VectorScore{{Vector: "v", Score: 1.5}}
	wrapped := JSONList[VectorScore](bare)

	a, _ := json.Marshal(bare)
	b, _ := json.Marshal(wrapped)
	if string(a) != string(b) {
		t.Fatalf("wire format drift: bare=%s wrapped=%s", a, b)
	}
}
