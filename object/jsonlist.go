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
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONList[T] is a []T that persists as a JSON-array text column.
//
// The data layer (github.com/hanzoai/dbx) writes/reads columns through
// database/sql. database/sql converts a Go value via
// driver.DefaultParameterConverter unless the value implements driver.Valuer —
// and that converter rejects a slice-of-struct outright with
// "unsupported type []T, a slice of struct". So a bare []struct field (e.g.
// message.SearchResults, message.ToolCalls) fails EVERY insert/update that
// touches the row — even when the slice is nil/empty, because the rejection is
// by TYPE, not value. That broke OAuth sign-in (the welcome-message insert) and
// every AI message save (which carries populated ToolCalls/SearchResults).
//
// JSONList is the slice-of-struct analogue of StringList: one generic type that
// implements sql.Scanner + driver.Valuer over JSON, so dbx routes any such
// column through JSON without per-type boilerplate or ORM changes. Its
// underlying type is []T, so it is assignable to/from []T and marshals to the
// same JSON array — call sites and the wire format are unchanged.
type JSONList[T any] []T

// Value encodes the list as JSON text for storage. nil/empty persist as "[]",
// matching StringList so NULL/empty round-trips are consistent across columns.
func (l JSONList[T]) Value() (driver.Value, error) {
	if len(l) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal([]T(l))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan decodes a stored column into the list. It accepts a JSON array
// ([{...}]), NULL (→ nil), and empty/"" (→ nil) — so existing rows (which the
// previous xorm-based store already persisted as JSON) load without a
// migration.
func (l *JSONList[T]) Scan(src interface{}) error {
	*l = nil
	if src == nil {
		return nil
	}

	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("JSONList.Scan: unsupported source type %T", src)
	}

	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, (*[]T)(l))
}
