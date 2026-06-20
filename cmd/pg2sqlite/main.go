// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

// Command pg2sqlite migrates cloud-api's dbx-managed Postgres database to a
// SQLite file. cloud-api uses raw dbx queries (not ORM struct sync), so the
// canonical schema lives in postgres only. This tool translates the live
// postgres schema + data into a SQLite file the cloud-api binary can open
// with driverName=sqlite (modernc.org/sqlite, already linked since v1.784.2).
//
// Usage:
//
//	pg2sqlite -src "user=hanzo password=... host=postgres port=5432 dbname=hanzo_cloud sslmode=disable" \
//	          -dst /tmp/cloud-staging.db
//
// Strategy:
//  1. Open source postgres via lib/pq, destination sqlite via modernc.org/sqlite.
//  2. Discover schema from information_schema (no pg_dump dependency).
//  3. Translate per-column types to SQLite equivalents.
//  4. Per-table CREATE TABLE on destination using composite PK from
//     information_schema.table_constraints (the cloud-api convention is
//     PRIMARY KEY (owner, name) on every table except `record` which uses
//     a synthetic INTEGER id).
//  5. Stream rows: SELECT * FROM source → INSERT into dest with parameter
//     binding (nil → NULL, []byte → BLOB, time.Time → RFC3339).
//  6. Emit a per-table row-count report so the operator can verify parity
//     before flipping driverName.
//
// Mirrors the IAM pg2sqlite design (~/work/hanzo/iam/cmd/pg2sqlite/main.go)
// but works without xorm because cloud-api never adopted xorm.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// pgToSqliteType maps a postgres data_type (as reported by
// information_schema.columns.data_type) to a SQLite affinity. The mapping
// is intentionally narrow — we only support the types actually present in
// hanzo_cloud (verified 2026-05-23):
//
//	bigint, boolean, character varying, double precision, integer,
//	json, numeric, real, text
//
// Any other type causes a fatal log; expanding support is a deliberate act,
// not a silent best-effort cast.
func pgToSqliteType(dt string) (string, error) {
	switch dt {
	case "bigint":
		return "BIGINT", nil
	case "boolean":
		return "INTEGER", nil // sqlite stores 0/1; dbx scans into bool fine
	case "character varying", "text":
		return "TEXT", nil
	case "double precision":
		return "REAL", nil
	case "integer":
		return "INTEGER", nil
	case "json", "jsonb":
		return "TEXT", nil // sqlite has json1 extension on TEXT
	case "numeric":
		return "NUMERIC", nil
	case "real":
		return "REAL", nil
	default:
		return "", fmt.Errorf("unsupported postgres type %q (extend pgToSqliteType to add)", dt)
	}
}

type colInfo struct {
	name       string
	dataType   string
	isNullable bool
	colDefault sql.NullString
	isIdentity bool // record.id uses nextval — translate to INTEGER PK AUTOINCREMENT
}

type tableInfo struct {
	name string
	cols []colInfo
	pk   []string
}

// fetchTables enumerates all base tables in the public schema, with
// columns and primary keys.
func fetchTables(db *sql.DB) ([]tableInfo, error) {
	rows, err := db.Query(`
		SELECT table_name
		  FROM information_schema.tables
		 WHERE table_schema = 'public'
		   AND table_type   = 'BASE TABLE'
		 ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]tableInfo, 0, len(names))
	for _, n := range names {
		ti := tableInfo{name: n}
		if err := loadColumns(db, &ti); err != nil {
			return nil, fmt.Errorf("table %s columns: %w", n, err)
		}
		if err := loadPrimaryKey(db, &ti); err != nil {
			return nil, fmt.Errorf("table %s pk: %w", n, err)
		}
		out = append(out, ti)
	}
	return out, nil
}

func loadColumns(db *sql.DB, ti *tableInfo) error {
	rows, err := db.Query(`
		SELECT column_name,
		       data_type,
		       is_nullable = 'YES' AS is_nullable,
		       column_default,
		       COALESCE(column_default LIKE 'nextval%', false) AS is_identity
		  FROM information_schema.columns
		 WHERE table_schema = 'public'
		   AND table_name   = $1
		 ORDER BY ordinal_position`, ti.name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c colInfo
		if err := rows.Scan(&c.name, &c.dataType, &c.isNullable, &c.colDefault, &c.isIdentity); err != nil {
			return err
		}
		ti.cols = append(ti.cols, c)
	}
	return rows.Err()
}

func loadPrimaryKey(db *sql.DB, ti *tableInfo) error {
	rows, err := db.Query(`
		SELECT kcu.column_name
		  FROM information_schema.table_constraints tc
		  JOIN information_schema.key_column_usage kcu
		    ON tc.constraint_name = kcu.constraint_name
		   AND tc.table_schema    = kcu.table_schema
		 WHERE tc.constraint_type = 'PRIMARY KEY'
		   AND tc.table_schema    = 'public'
		   AND tc.table_name      = $1
		 ORDER BY kcu.ordinal_position`, ti.name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return err
		}
		ti.pk = append(ti.pk, c)
	}
	return rows.Err()
}

// emitCreate renders a CREATE TABLE for SQLite. Composite PRIMARY KEY lives
// at table level (not column level) so it works on any number of cols. The
// `record.id` case auto-detects column_default starting with "nextval" and
// emits "INTEGER PRIMARY KEY AUTOINCREMENT" (the canonical SQLite shape).
func emitCreate(ti tableInfo) (string, error) {
	var parts []string
	singleIntPK := len(ti.pk) == 1 && len(ti.cols) > 0 && ti.cols[0].isIdentity && ti.cols[0].name == ti.pk[0]
	for _, c := range ti.cols {
		sqliteType, err := pgToSqliteType(c.dataType)
		if err != nil {
			return "", fmt.Errorf("%s.%s: %w", ti.name, c.name, err)
		}
		def := fmt.Sprintf(`"%s" %s`, c.name, sqliteType)
		if singleIntPK && c.isIdentity {
			def = fmt.Sprintf(`"%s" INTEGER PRIMARY KEY AUTOINCREMENT`, c.name)
		} else if !c.isNullable {
			def += " NOT NULL"
		}
		parts = append(parts, "    "+def)
	}
	if !singleIntPK && len(ti.pk) > 0 {
		quoted := make([]string, len(ti.pk))
		for i, p := range ti.pk {
			quoted[i] = fmt.Sprintf(`"%s"`, p)
		}
		parts = append(parts, fmt.Sprintf("    PRIMARY KEY (%s)", strings.Join(quoted, ", ")))
	}
	return fmt.Sprintf("CREATE TABLE \"%s\" (\n%s\n);", ti.name, strings.Join(parts, ",\n")), nil
}

// copyTable streams all rows from src.<table> into dst.<table> in a single
// transaction. Uses INSERT OR IGNORE so postgres-side duplicate-pkey rows
// (real-world: occasionally seen on hanzo-k8s when an out-of-order migration
// repopulates seed data) drop silently with a report-side delta.
func copyTable(src, dst *sql.DB, ti tableInfo) (copied, dropped int64, err error) {
	colNames := make([]string, len(ti.cols))
	for i, c := range ti.cols {
		colNames[i] = c.name
	}
	srcCols := `"` + strings.Join(colNames, `","`) + `"`
	rows, err := src.Query(fmt.Sprintf(`SELECT %s FROM %q`, srcCols, ti.name))
	if err != nil {
		return 0, 0, fmt.Errorf("select: %w", err)
	}
	defer rows.Close()

	placeholders := make([]string, len(colNames))
	for i := range colNames {
		placeholders[i] = "?"
	}
	insertSQL := fmt.Sprintf(`INSERT OR IGNORE INTO "%s" (%s) VALUES (%s)`,
		ti.name, srcCols, strings.Join(placeholders, ","))

	tx, err := dst.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		return 0, 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for rows.Next() {
		raw := make([]interface{}, len(colNames))
		ptrs := make([]interface{}, len(colNames))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return copied, dropped, fmt.Errorf("scan: %w", err)
		}
		args := make([]interface{}, len(raw))
		for i, v := range raw {
			args[i] = coerceForSQLite(v)
		}
		res, ierr := stmt.Exec(args...)
		if ierr != nil {
			err = fmt.Errorf("insert %s: %w", ti.name, ierr)
			return copied, dropped, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			dropped++
		} else {
			copied++
		}
	}
	if err = rows.Err(); err != nil {
		return copied, dropped, fmt.Errorf("scan: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return copied, dropped, fmt.Errorf("commit: %w", err)
	}
	return copied, dropped, nil
}

// coerceForSQLite normalises lib/pq oddities so modernc.org/sqlite stores
// them losslessly:
//   - time.Time -> RFC3339Nano string (cloud-api stores all temporal data as varchar)
//   - []byte    -> BLOB (kept as-is)
//   - nil       -> NULL
func coerceForSQLite(v interface{}) interface{} {
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		if x.IsZero() {
			return nil
		}
		return x.UTC().Format(time.RFC3339Nano)
	case []byte:
		return x
	default:
		return v
	}
}

func main() {
	src := flag.String("src", "", "Postgres DSN (lib/pq KV form, e.g. \"user=... host=... dbname=hanzo_cloud sslmode=disable\")")
	dst := flag.String("dst", "/tmp/cloud-staging.db", "Destination SQLite file path")
	overwrite := flag.Bool("overwrite", false, "Delete destination SQLite file if it already exists")
	verifyOnly := flag.Bool("verify", false, "Skip writes; only emit per-table row counts of both sides")
	flag.Parse()

	if *src == "" {
		log.Fatal("-src DSN required")
	}

	if !*verifyOnly {
		if _, err := os.Stat(*dst); err == nil {
			if !*overwrite {
				log.Fatalf("destination %s already exists; pass -overwrite to replace", *dst)
			}
			if err := os.Remove(*dst); err != nil {
				log.Fatalf("remove existing destination: %v", err)
			}
		}
	}

	srcDB, err := sql.Open("postgres", *src)
	if err != nil {
		log.Fatalf("open source: %v", err)
	}
	defer srcDB.Close()
	if err := srcDB.Ping(); err != nil {
		log.Fatalf("source ping: %v", err)
	}

	// SQLite DSN mirrors the runtime knobs the cloud-api binary will use
	// when it opens this same file. Keeping the pragmas identical avoids
	// any divergence between the file we copy and the file the server opens.
	// modernc.org/sqlite only honors the _pragma=NAME(VAL) form; the
	// mattn-style _busy_timeout/_journal_mode keys are silently ignored.
	dstDSN := fmt.Sprintf("file:%s?cache=shared&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)", *dst)
	dstDB, err := sql.Open("sqlite", dstDSN)
	if err != nil {
		log.Fatalf("open destination: %v", err)
	}
	defer dstDB.Close()
	if err := dstDB.Ping(); err != nil {
		log.Fatalf("dest ping: %v", err)
	}

	tables, err := fetchTables(srcDB)
	if err != nil {
		log.Fatalf("fetch tables: %v", err)
	}

	if !*verifyOnly {
		log.Printf("creating %d tables on destination", len(tables))
		for _, t := range tables {
			ddl, derr := emitCreate(t)
			if derr != nil {
				log.Fatalf("emit DDL %s: %v", t.name, derr)
			}
			if _, err := dstDB.Exec(ddl); err != nil {
				log.Fatalf("create %s: %v\n%s", t.name, err, ddl)
			}
		}
	}

	type report struct {
		name    string
		srcRows int64
		dstRows int64
		copied  int64
		dropped int64
		errStr  string
	}
	reps := make([]report, 0, len(tables))

	start := time.Now()
	for _, t := range tables {
		r := report{name: t.name}
		if err := srcDB.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %q`, t.name)).Scan(&r.srcRows); err != nil {
			r.errStr = "src count: " + err.Error()
			reps = append(reps, r)
			continue
		}
		if *verifyOnly {
			_ = dstDB.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, t.name)).Scan(&r.dstRows)
			reps = append(reps, r)
			continue
		}
		copied, dropped, cerr := copyTable(srcDB, dstDB, t)
		r.copied = copied
		r.dropped = dropped
		if cerr != nil {
			r.errStr = cerr.Error()
		}
		_ = dstDB.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, t.name)).Scan(&r.dstRows)
		reps = append(reps, r)
	}

	sort.Slice(reps, func(i, j int) bool { return reps[i].name < reps[j].name })

	fmt.Fprintf(os.Stderr, "\n=== pg2sqlite report (%s) ===\n", time.Since(start).Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "%-20s %10s %10s %10s %10s   %s\n", "table", "src", "copied", "dropped", "dst", "note")
	var mismatch, dirty int
	for _, r := range reps {
		note := ""
		if r.errStr != "" {
			note = "ERROR: " + r.errStr
			mismatch++
		} else if !*verifyOnly && r.dropped > 0 {
			note = fmt.Sprintf("DEDUPED %d", r.dropped)
			dirty++
		} else if !*verifyOnly && r.srcRows != r.dstRows {
			note = "ROW MISMATCH"
			mismatch++
		} else if *verifyOnly && r.srcRows != r.dstRows {
			note = "(verify diff)"
		}
		fmt.Fprintf(os.Stderr, "%-20s %10d %10d %10d %10d   %s\n", r.name, r.srcRows, r.copied, r.dropped, r.dstRows, note)
	}
	fmt.Fprintln(os.Stderr)
	if mismatch > 0 && !*verifyOnly {
		fmt.Fprintf(os.Stderr, "FAIL: %d table(s) reported errors — destination SQLite is NOT safe to deploy\n", mismatch)
		os.Exit(2)
	}
	if !*verifyOnly && dirty > 0 {
		fmt.Fprintf(os.Stderr, "WARN: %d table(s) had postgres-side duplicate rows dropped on migration. Review the report.\n", dirty)
	}
	if !*verifyOnly {
		fmt.Fprintf(os.Stderr, "OK: %d tables copied to %s\n", len(reps), *dst)
	}
}
