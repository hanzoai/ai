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

// JSONMap[V] is a map[string]V that persists as a JSON-object text column.
//
// The map analogue of JSONList: database/sql's default converter rejects a map
// of slices outright, so a bare map[string][]string field (e.g. an org's router
// preference table) would fail every insert/update that touches the row.
// JSONMap implements sql.Scanner + driver.Valuer over JSON, so dbx routes the
// column through JSON. Its underlying type is map[string]V, so it is
// assignable to/from the bare map and marshals to the same JSON object.
type JSONMap[V any] map[string]V

// Value encodes the map as JSON text. nil/empty persist as "{}".
func (m JSONMap[V]) Value() (driver.Value, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(map[string]V(m))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan decodes a stored column into the map. Accepts a JSON object, NULL, and
// empty/"" (all -> nil), so a column added after the table existed (no value
// for old rows) loads without a migration.
func (m *JSONMap[V]) Scan(src interface{}) error {
	*m = nil
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
		return fmt.Errorf("JSONMap.Scan: unsupported source type %T", src)
	}
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, (*map[string]V)(m))
}
