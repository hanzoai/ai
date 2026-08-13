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
	"regexp"
	"testing"
)

// The schema is stated three ways that must agree: the CREATE, the ADD COLUMNs
// that bring an old table up to it, and the column list the writer fills. Comments
// on each say "keep in lockstep". A comment cannot.
//
// The drift it misses is silent and one-directional: add a column to the CREATE,
// forget the writer, and everything still compiles, still runs, still writes rows.
// The column is simply always empty — and nothing distinguishes "nobody has used
// this yet" from "we never wrote it". A question the column exists to answer gets
// a confident, wrong "no data" for as long as it takes someone to suspect the pipe
// instead of the traffic.
//
// That is not hypothetical here: origin, agent, api_key_hash and session_id were
// declared, documented, and populated on the record, and written by nothing.

var (
	ddlColumn = regexp.MustCompile(`(?m)^\s+([a-z_]+)\s+(?:String|DateTime|UInt8|UInt32|UInt64|Int64)`)
	addColumn = regexp.MustCompile(`ADD COLUMN IF NOT EXISTS\s+([a-z_]+)`)
)

func ddlColumns(t *testing.T) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	for _, m := range ddlColumn.FindAllStringSubmatch(cloudUsageTableDDL, -1) {
		cols[m[1]] = true
	}
	if len(cols) < 20 {
		t.Fatalf("parsed %d columns from the DDL — the parser is wrong, not the schema", len(cols))
	}
	return cols
}

// TestMigrationsCoverEveryColumn: a table created before a column existed only
// gains it through an ADD COLUMN. A column in the CREATE with no migration works
// perfectly on a fresh database and is absent from every one that predates it —
// which is every database holding the history anyone wants to query.
func TestMigrationsCoverEveryColumn(t *testing.T) {
	migrated := map[string]bool{}
	for _, stmt := range cloudUsageColumnMigrations {
		m := addColumn.FindStringSubmatch(stmt)
		if m == nil {
			t.Fatalf("migration is not an ADD COLUMN IF NOT EXISTS: %q", stmt)
		}
		migrated[m[1]] = true
	}

	// The original table's columns need no migration — they have always been there.
	original := map[string]bool{
		"id": true, "timestamp": true, "owner": true, "user_id": true,
		"organization": true, "model": true, "provider": true, "request_id": true,
		"prompt_tokens": true, "completion_tokens": true, "total_tokens": true,
		"cache_read_tokens": true, "cache_write_tokens": true, "cost_cents": true,
		"currency": true, "status": true, "error_msg": true, "is_premium": true,
		"is_stream": true, "client_ip": true,
	}

	for col := range ddlColumns(t) {
		if original[col] || migrated[col] {
			continue
		}
		t.Errorf("column %q is in the CREATE with no ADD COLUMN: a fresh database "+
			"gets it and every existing one silently does not", col)
	}
}

// TestWriterFillsEveryColumn is the one that would have caught the real bug.
func TestWriterFillsEveryColumn(t *testing.T) {
	declared := ddlColumns(t)
	written := map[string]bool{}

	for _, c := range CloudUsageColumns {
		if written[c] {
			t.Errorf("column %q is written twice", c)
		}
		written[c] = true
		if !declared[c] {
			t.Errorf("the writer fills %q, which the schema does not declare", c)
		}
	}

	// The other direction is the silent one, so it fails loudly here.
	for col := range declared {
		if !written[col] {
			t.Errorf("%q is declared and never written: it reads as empty forever, "+
				"and empty is indistinguishable from unused", col)
		}
	}
}

// TestInsertMatchesColumns pins the derivation itself: one placeholder per column,
// so a value can never land in its neighbour's field.
func TestInsertMatchesColumns(t *testing.T) {
	got := 0
	for _, r := range CloudUsageInsert {
		if r == '?' {
			got++
		}
	}
	if got != len(CloudUsageColumns) {
		t.Fatalf("%d placeholders for %d columns", got, len(CloudUsageColumns))
	}
}
