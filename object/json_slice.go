// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Generic SQL Scanner / driver.Valuer for slice and map types stored as
// JSON in TEXT columns. Required because Go's database/sql can't scan a
// string straight into []string / map[string]any without the destination
// type implementing the Scanner interface.

package object

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// StringSlice is []string with JSON marshalling for SQL TEXT columns.
type StringSlice []string

func (s *StringSlice) Scan(src interface{}) error {
	if src == nil {
		*s = nil
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case string:
		if v == "" {
			*s = nil
			return nil
		}
		data = []byte(v)
	case []byte:
		if len(v) == 0 {
			*s = nil
			return nil
		}
		data = v
	default:
		return fmt.Errorf("StringSlice.Scan: unsupported type %T", src)
	}
	return json.Unmarshal(data, s)
}

func (s StringSlice) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}
