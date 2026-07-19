// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"fmt"
	"reflect"
	"strings"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/dbx"
)

var scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()

// backfillNullScalars repairs rows that predate a column's addition, so a full read
// of the model never trips over SQL NULL.
//
// WHY THIS EXISTS. dbx.Sync grows a table by `ALTER TABLE … ADD COLUMN` with NO
// default (a struct field carries no `default:` tag), so every row that existed
// before a column was added holds SQL NULL there. dbx then scans a row by handing
// database/sql a *string / *float64 per struct field, and database/sql refuses NULL
// into a non-pointer scalar ("converting NULL to string is unsupported") — which
// fails the WHOLE read, not just that field. That is exactly what broke the router
// trainer's OrgSettings reads (contributor list, per-org deploy-prefer, consent
// seed) after router_strategy / router_mean_field_enabled were added: pre-existing
// "*"/"admin"/"hanzo" rows were NULL there.
//
// New rows are already safe — dbx Insert writes the Go zero value for every column —
// so only the pre-existing rows need repair, once, idempotently (`WHERE col IS NULL`
// is a no-op on a clean row).
//
// Schema-driven: the columns come from the model's own scalar fields via the SAME
// dbx.DefaultFieldMapFunc that Sync used to create them, so a column added in a
// future release is covered with no code change and the names cannot drift. Fields
// that implement sql.Scanner (the JSON/slice columns) tolerate NULL themselves and
// are skipped, as is the primary key. Best-effort: a per-column failure logs and
// never blocks boot.
func backfillNullScalars(db *dbx.DB, table string, model interface{}) {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		col, ok := backfillColumn(f)
		if !ok {
			continue
		}
		zero, ok := zeroLiteral(f.Type)
		if !ok {
			continue
		}
		stmt := fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s IS NULL", table, col, zero, col)
		if _, err := db.NewQuery(stmt).Execute(); err != nil {
			log.Warning("backfill %s.%s: %v", table, col, err)
		}
	}
}

// backfillColumn resolves the DB column a field maps to, or reports skip=false when
// the field is not a backfillable column (unpersisted `db:"-"` or the primary key —
// a PK is never NULL). It mirrors dbx's own db-tag parsing so the column name is
// byte-identical to what Sync created.
func backfillColumn(f reflect.StructField) (col string, ok bool) {
	tag := f.Tag.Get("db")
	switch {
	case tag == "-":
		return "", false // not persisted
	case tag == "pk", strings.HasPrefix(tag, "pk,"):
		return "", false // primary key — never NULL
	case tag != "":
		return tag, true // explicit column name
	default:
		return dbx.DefaultFieldMapFunc(f.Name), true
	}
}

// zeroLiteral returns the SQL literal for a scalar Go type's zero value, or ok=false
// for a type that is not a plain nullable scalar. A field whose pointer implements
// sql.Scanner (JSON/slice columns — dbx scans into `field.Addr()`, so that is the
// dispatch database/sql uses) handles NULL itself and is skipped.
func zeroLiteral(t reflect.Type) (string, bool) {
	if reflect.PointerTo(t).Implements(scannerType) || t.Implements(scannerType) {
		return "", false
	}
	switch t.Kind() {
	case reflect.String:
		return "''", true
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "0", true
	default:
		return "", false // structs (time.Time), pointers, maps, slices — not a plain scalar
	}
}
