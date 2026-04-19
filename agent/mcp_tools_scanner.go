// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// SQL Scanner / driver.Valuer for the McpTools slice type so it round-trips
// through database/sql columns (TEXT-typed) as JSON.
//
// Without this, `Provider.McpTools []*McpTools` panics at startup with:
//
//	sql: Scan error on column index 15, name "mcp_tools": unsupported Scan,
//	storing driver.Value type string into type *[]*agent.McpTools
//
// xorm/dbx call Scan(stringValue) for TEXT columns; Go's database/sql can't
// directly populate a slice from a string, so we marshal/unmarshal JSON.

package agent

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// McpToolsList is a typed alias for []*McpTools that implements
// sql.Scanner + driver.Valuer. Use it in DB-mapped structs.
type McpToolsList []*McpTools

// Scan implements sql.Scanner for the slice. Accepts string, []byte, or nil.
func (l *McpToolsList) Scan(src interface{}) error {
	if src == nil {
		*l = nil
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case string:
		if v == "" {
			*l = nil
			return nil
		}
		data = []byte(v)
	case []byte:
		if len(v) == 0 {
			*l = nil
			return nil
		}
		data = v
	default:
		return fmt.Errorf("McpToolsList.Scan: unsupported type %T", src)
	}
	return json.Unmarshal(data, l)
}

// Value implements driver.Valuer. Returns JSON string (empty list as `[]`).
func (l McpToolsList) Value() (driver.Value, error) {
	if l == nil {
		return "[]", nil
	}
	b, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// ScanInto is a helper for receiving any value into a *[]*McpTools when
// the field can't easily be retyped to McpToolsList.
func ScanInto(dst *[]*McpTools, src interface{}) error {
	if dst == nil {
		return errors.New("McpToolsList ScanInto: nil destination")
	}
	var l McpToolsList
	if err := (&l).Scan(src); err != nil {
		return err
	}
	*dst = []*McpTools(l)
	return nil
}
