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
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/hanzoai/dbx"
)

// emptyStore is a database with no tables, on whichever engine the suite runs
// (SQLite, or the server TestSQLEnv names).
func emptyStore(t *testing.T) *dbx.DB {
	t.Helper()
	restore, err := UseMemoryDB(fmt.Sprintf("file:schema_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)
	return adapter.db
}

// every model SyncSchema keeps, as a fresh pointer each call.
func modelsOf(t *testing.T) []any {
	t.Helper()
	return []any{
		&Application{}, &Article{}, &Asset{}, &Chat{}, &Connection{},
		&File{}, &Form{}, &Graph{}, &Message{}, &ModelRoute{}, &Node{},
		&Provider{}, &Record{}, &Scale{}, &Scan{}, &Session{}, &Store{},
		&Task{}, &Template{}, &Vector{}, &Video{}, &Workflow{},
		&Memory{}, &OrgSettings{}, &RoutingEvent{}, &RouterArtifactMeta{},
		&RouterTrainingLog{}, &FinetuneJob{}, &ModelAccess{}, &RoutingVersion{},
		&Listing{}, &ListingEvent{}, &Lease{}, &Served{}, &MediaJob{},
	}
}

// TestSyncSchemaMakesEveryTableAndMakesThemTwice: the host runs SyncSchema as the
// open of ai's store, on the file and again on the schema a move fills, so it has to
// succeed on a database that has the tables as on one that has not, and a row of every
// model has to go in and come back out of what it made.
func TestSyncSchemaMakesEveryTableAndMakesThemTwice(t *testing.T) {
	db := emptyStore(t)
	for range 2 {
		if err := SyncSchema(db); err != nil {
			t.Fatalf("SyncSchema: %v", err)
		}
	}
	for _, m := range modelsOf(t) {
		rt := reflect.TypeOf(m).Elem()
		if err := insertRow(db, m); err != nil {
			t.Errorf("%s: insert: %v", rt.Name(), err)
			continue
		}
		back := reflect.New(rt).Interface()
		if err := db.Select().From(dbx.DefaultFieldMapFunc(rt.Name())).One(back); err != nil {
			t.Errorf("%s: read back: %v", rt.Name(), err)
		}
	}
}

// TestAnIntegerKeyIsNumberedByTheDatabase: dbx leaves a zero integer key out of the
// insert and reads the number back, which SQLite's rowid answers and a PostgreSQL
// column answers only if it is an identity.
func TestAnIntegerKeyIsNumberedByTheDatabase(t *testing.T) {
	db := emptyStore(t)
	if err := SyncSchema(db); err != nil {
		t.Fatal(err)
	}
	a, b := &RoutingVersion{}, &RoutingVersion{}
	for _, v := range []*RoutingVersion{a, b} {
		if err := insertRow(db, v); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if a.Id == 0 || b.Id <= a.Id {
		t.Fatalf("ids = %d, %d: want two rising numbers", a.Id, b.Id)
	}
	// A row carried in keeps its number, and the next one follows it.
	c := &RoutingVersion{Id: b.Id + 100}
	if err := insertRow(db, c); err != nil {
		t.Fatalf("insert with number: %v", err)
	}
}

// TestWideNumbersAndBooleansSurvive: PostgreSQL's INTEGER is 32 bits and its REAL 4
// bytes, and a bool is refused by an integer column, none of which SQLite minds.
func TestWideNumbersAndBooleansSurvive(t *testing.T) {
	db := emptyStore(t)
	if err := SyncSchema(db); err != nil {
		t.Fatal(err)
	}
	const wide = int64(5_000_000_000)
	if err := CountServed("v", "m", "k", time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Update("served", dbx.Params{"answered": wide}, dbx.HashExp{"vendor": "v"}).Execute(); err != nil {
		t.Fatal(err)
	}
	rows, err := ServedSince("v", time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 1 || rows[0].Answered != wide {
		t.Fatalf("served = %+v, %v: want answered %d", rows, err, wide)
	}

	r := &ModelRoute{Owner: "o", ModelName: "m", Priced: true}
	if err := insertRow(db, r); err != nil {
		t.Fatalf("insert route: %v", err)
	}
	var got ModelRoute
	if err := db.Select().From("model_route").One(&got); err != nil || !got.Priced {
		t.Fatalf("route = %+v, %v: want priced", got, err)
	}
}

// TestLeaseAndServedUpsertOnEitherEngine: the two statements written by hand.
func TestLeaseAndServedUpsertOnEitherEngine(t *testing.T) {
	emptyStore(t)
	if err := SyncSchema(adapter.db); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	taken, at, err := TakeLease("job", time.Minute, now)
	if err != nil || !taken {
		t.Fatalf("first take = %v, %v", taken, err)
	}
	if again, _, err := TakeLease("job", time.Minute, now); err != nil || again {
		t.Fatalf("second take = %v, %v: want refused", again, err)
	}
	if err := ReleaseLease("job", at); err != nil {
		t.Fatal(err)
	}
	if again, _, err := TakeLease("job", time.Minute, now); err != nil || !again {
		t.Fatalf("take after release = %v, %v", again, err)
	}
	for range 3 {
		if err := CountServed("v", "m", "k", now, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := CountServed("v", "m", "k", now, false); err != nil {
		t.Fatal(err)
	}
	rows, err := ServedSince("v", now.Add(-time.Hour))
	if err != nil || len(rows) != 1 || rows[0].Answered != 3 || rows[0].Failed != 1 {
		t.Fatalf("served = %+v, %v: want 3 answered, 1 failed", rows, err)
	}
	if err := DropServed(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

// TestAColumnAddedLaterIsGrown: a table made by an earlier release lacks a column
// the model has now, and SyncSchema adds it.
func TestAColumnAddedLaterIsGrown(t *testing.T) {
	db := emptyStore(t)
	if err := SyncSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.NewQuery(`ALTER TABLE "served" DROP COLUMN "failed"`).Execute(); err != nil {
		t.Fatal(err)
	}
	if err := SyncSchema(db); err != nil {
		t.Fatalf("SyncSchema over the old table: %v", err)
	}
	if err := CountServed("v", "m", "k", time.Now(), false); err != nil {
		t.Fatalf("the grown column is not there: %v", err)
	}
}

// TestUseDBHandsOverTheHostsDatabase: InitAdapter opens nothing from config once a
// host has handed a database, and uses the host's.
func TestUseDBHandsOverTheHostsDatabase(t *testing.T) {
	db := emptyStore(t)
	prev := adapter
	t.Cleanup(func() { adapter = prev; UseDB(nil) })
	UseDB(db)
	adapter = nil
	InitAdapter()
	if adapter == nil || adapter.db != db {
		t.Fatalf("adapter = %+v: want the host's database", adapter)
	}
	if adapter.driverName != db.DriverName() {
		t.Fatalf("driver = %q, want %q", adapter.driverName, db.DriverName())
	}
	CreateTables()
	if err := insertRow(db, &Lease{Name: "x"}); err != nil {
		t.Fatalf("the host's database lacks ai's tables: %v", err)
	}
}
