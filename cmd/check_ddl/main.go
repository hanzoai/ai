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

package main

import (
	"database/sql"
	"fmt"

	_ "github.com/hanzoai/sqlite" // ONE Hanzo sqlite driver (cgo→SQLCipher, !cgo→modernc); never import modernc directly
)

func main() {
	db, _ := sql.Open("sqlite", "file:/tmp/cloud-staging.db?mode=ro&_journal_mode=OFF")
	rows, _ := db.Query("SELECT name, sql FROM sqlite_master WHERE type='table' ORDER BY name")
	for rows.Next() {
		var n, sqlStr string
		rows.Scan(&n, &sqlStr)
		fmt.Printf("=== %s ===\n%s\n\n", n, sqlStr)
	}
	for _, t := range []string{"application", "record", "provider", "store", "template"} {
		var c int
		db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", t)).Scan(&c)
		fmt.Printf("rows in %s: %d\n", t, c)
	}
}
