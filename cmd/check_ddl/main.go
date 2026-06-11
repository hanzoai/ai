package main

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
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
