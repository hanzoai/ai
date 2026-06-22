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
	"strings"
)

// StringList is a []string that persists as a JSON-array text column.
//
// The data layer (github.com/hanzoai/dbx) scans a text/varchar column into a
// struct field via database/sql. A bare []string field is not scannable, so any
// row holding such a column fails with "unsupported Scan ... string into
// *[]string". StringList implements sql.Scanner + driver.Valuer so dbx routes
// these columns through JSON — one type fixes every list column (chat.Users,
// message.LikeUsers, …) without touching the ORM.
//
// Its underlying type is []string, so it is assignable to/from []string and
// marshals to the same JSON array — callers and the wire format are unchanged.
type StringList []string

// Value encodes the list as JSON text for storage. nil/empty persist as "[]".
func (l StringList) Value() (driver.Value, error) {
	if len(l) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal([]string(l))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan decodes a stored column into the list. It accepts the modern JSON-array
// form (["a","b"]), the legacy comma-separated/single-value form written by the
// previous xorm-based store, and NULL/empty (→ nil) — so existing rows load
// without a migration.
func (l *StringList) Scan(src interface{}) error {
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
		return fmt.Errorf("StringList.Scan: unsupported source type %T", src)
	}

	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}

	if b[0] == '[' {
		return json.Unmarshal(b, (*[]string)(l))
	}

	// Legacy comma-separated (or single) value.
	parts := strings.Split(string(b), ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	*l = out
	return nil
}
