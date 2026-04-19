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

// JSONScan is a helper for fields that are scanned-from-JSON-text via a
// pointer-receiver Scan method. Use it inside per-type Scanner impls so
// they all share the same string/bytes/nil handling.
func JSONScan(dst interface{}, src interface{}) error {
	if src == nil {
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case string:
		if v == "" {
			return nil
		}
		data = []byte(v)
	case []byte:
		if len(v) == 0 {
			return nil
		}
		data = v
	default:
		return fmt.Errorf("JSONScan: unsupported type %T", src)
	}
	return json.Unmarshal(data, dst)
}

// JSONValue marshals v as a JSON string for driver.Valuer impls.
func JSONValue(v interface{}) (driver.Value, error) {
	if v == nil {
		return "null", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// ExampleQuestionList implements sql.Scanner for slice-of-ExampleQuestion,
// stored as JSON in a TEXT column.
type ExampleQuestionList []ExampleQuestion

func (l *ExampleQuestionList) Scan(src interface{}) error  { return JSONScan(l, src) }
func (l ExampleQuestionList) Value() (driver.Value, error) { return JSONValue(l) }

// PropertiesMap implements sql.Scanner for map[string]*Properties.
type PropertiesMapJSON map[string]*Properties

func (m *PropertiesMapJSON) Scan(src interface{}) error  { return JSONScan(m, src) }
func (m PropertiesMapJSON) Value() (driver.Value, error) { return JSONValue(m) }

// TreeFilePtr implements sql.Scanner for *TreeFile.
type TreeFilePtr struct{ *TreeFile }

func (t *TreeFilePtr) Scan(src interface{}) error {
	if src == nil {
		return nil
	}
	var tf TreeFile
	if err := JSONScan(&tf, src); err != nil {
		return err
	}
	t.TreeFile = &tf
	return nil
}

func (t TreeFilePtr) Value() (driver.Value, error) {
	if t.TreeFile == nil {
		return "null", nil
	}
	return JSONValue(t.TreeFile)
}
