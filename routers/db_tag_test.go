// Copyright © 2026 Hanzo AI. MIT License.

package routers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// A `db` tag is a COLUMN NAME, and xorm's vocabulary is not a directive here.
//
// dbx's parseTag (struct.go:229) special-cases exactly two forms — "pk" and
// "pk,<name>" — and returns everything else VERBATIM as the column name. So a tag
// carried over from xorm configures nothing; it silently RENAMES the column:
//
//	`db:"index"`               -> a column named `index`  (a reserved SQL word)
//	`db:"json varchar(1000)"`  -> a column named `json varchar(1000)`, spaces and all
//
// The failure is invisible at compile time and nearly invisible at runtime, because
// the ORM writes and reads the SAME wrong column and stays self-consistent. It
// surfaces only where raw SQL names the column the Go field implies.
// object.Record.NeedCommit carried `db:"index"`, so ScanNeedCommitRecords'
// `need_commit = {:p0}` failed with "no such column: need_commit" every five
// minutes in production, and the record-chain commit task had never committed a
// single record.
//
// This lives in routers, not object, deliberately: object's TestMain os.Exit(0)s
// the whole package when no seeded database is present, so a guard placed there
// would report "ok" without ever running. A schema-shape invariant needs no
// database and must not be gated behind one.
func TestDbTagIsAColumnNameNotAXormDirective(t *testing.T) {
	// xorm tag vocabulary. None of it means anything to dbx; each becomes a column
	// name. "json" is the one that reads most like a legitimate name — which is
	// exactly why it has to be listed.
	xormWords := map[string]bool{
		"index": true, "unique": true, "json": true, "notnull": true, "null": true,
		"autoincr": true, "created": true, "updated": true, "deleted": true,
		"version": true, "extends": true, "default": true, "comment": true,
	}

	// Two tags predate this guard and cannot simply be dropped: dbx quotes the
	// column name (sync.go createTable -> quoteIdent), so `json varchar(1000)` and
	// `json` are REAL created columns that may hold real rows. Renaming them to the
	// field's snake_case name creates a fresh empty column and the old values stop
	// being read — a data migration, not a tag edit. They are listed so this guard
	// stays green for existing debt while still failing on anything NEW, and so the
	// debt is named rather than forgotten. Removing an entry requires copying the
	// old column into the new one first.
	needsMigration := map[string]string{
		"Operations":         `json varchar(1000)`, // object/connection.go
		"BasicConfigOptions": `json`,               // object/template.go
	}

	files, err := filepath.Glob(filepath.Join("..", "object", "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no object/*.go found — this guard would silently pass over nothing")
	}

	fset := token.NewFileSet()
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			continue // the compiler is the right reporter for that
		}
		scanned++

		ast.Inspect(file, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok || field.Tag == nil || len(field.Names) == 0 {
				return true
			}
			raw, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				return true
			}
			tag, ok := reflect.StructTag(raw).Lookup("db")
			if !ok {
				return true
			}
			if tag == "pk" || tag == "-" || strings.HasPrefix(tag, "pk,") {
				return true // the only forms dbx interprets
			}

			name := field.Names[0].Name
			where := fset.Position(field.Pos())

			if legacy, ok := needsMigration[name]; ok && legacy == tag {
				t.Logf("%s: %s still has db:%q — known debt, needs a column migration "+
					"before the tag can be dropped", where, name, tag)
				return true
			}

			if strings.ContainsAny(tag, " \t(),") {
				t.Errorf("%s: %s has db:%q — dbx takes that VERBATIM as the column name, "+
					"so it creates a column with spaces or parens in it. Drop the tag; the "+
					"field's snake_case name is what raw SQL already assumes.",
					where, name, tag)
				return true
			}
			if xormWords[strings.ToLower(tag)] {
				t.Errorf("%s: %s has db:%q — xorm vocabulary, which dbx has no notion of: "+
					"it names the column %q. Drop the tag so the column is the field's "+
					"snake_case name.", where, name, tag, tag)
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("parsed no object sources — the guard covered nothing")
	}
}

// The specific column the record-chain task queries, named so a regression is
// recognizable rather than merely red.
func TestRecordNeedCommitMapsToNeedCommitColumn(t *testing.T) {
	f, ok := reflect.TypeOf(object.Record{}).FieldByName("NeedCommit")
	if !ok {
		t.Fatal("object.Record has no NeedCommit field")
	}
	if tag, present := f.Tag.Lookup("db"); present {
		t.Fatalf("NeedCommit has db:%q — ScanNeedCommitRecords queries `need_commit`, "+
			"so any db tag here renames the column out from under it", tag)
	}
}
