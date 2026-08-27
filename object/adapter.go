// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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
	"flag"
	"fmt"
	"runtime"

	_ "github.com/go-sql-driver/mysql" // mysql
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/dbx"
	_ "github.com/hanzoai/sqlite"       // sqlite: the ONE Hanzo driver — registers "sqlite" exactly once (cgo→mattn/SQLCipher, !cgo→modernc). Never import modernc directly, or a cgo build double-registers "sqlite" and panics at init.
	_ "github.com/lib/pq"               // postgres
	_ "github.com/microsoft/go-mssqldb" // mssql (maintained successor to denisenkom; one mssql driver registration across the stack)
)

var (
	adapter                 *Adapter = nil
	providerAdapter         *Adapter = nil
	isCreateDatabaseDefined          = false
	createDatabase                   = true
)

func InitFlag() {
	if !isCreateDatabaseDefined {
		isCreateDatabaseDefined = true
		createDatabase = getCreateDatabaseFlag()
	}
}

func getCreateDatabaseFlag() bool {
	res := flag.Bool("createDatabase", false, "true if you need to create database")
	flag.Parse()
	return *res
}

func InitConfig() {
	err := conf.LoadAppConfig("ini", "../conf/app.conf")
	if err != nil {
		panic(err)
	}
	InitAdapter()
	CreateTables()
}

func InitAdapter() {
	adapter = NewAdapter(conf.GetConfigString("driverName"), conf.GetConfigDataSourceName())
	providerDbName := conf.GetConfigString("providerDbName")
	if adapter.DbName == providerDbName {
		providerDbName = ""
	}
	if providerDbName != "" {
		providerAdapter = NewAdapterWithDbName(conf.GetConfigString("driverName"), conf.GetConfigDataSourceName(), providerDbName)
	}
}

func CreateTables() {
	if createDatabase {
		err := adapter.CreateDatabase()
		if err != nil {
			panic(err)
		}
	}
	adapter.createTable()
}

// Adapter represents the database adapter for storage.
type Adapter struct {
	driverName     string
	dataSourceName string
	DbName         string
	db             *dbx.DB
}

// finalizer is the destructor for Adapter.
func finalizer(a *Adapter) {
	err := a.db.Close()
	if err != nil {
		panic(err)
	}
}

// NewAdapter is the constructor for Adapter.
func NewAdapter(driverName string, dataSourceName string) *Adapter {
	a := &Adapter{}
	a.driverName = driverName
	a.dataSourceName = dataSourceName
	a.DbName = conf.GetConfigString("dbName")
	// Open the DB, create it if not existed.
	a.open()
	// Call the destructor when the object is released.
	runtime.SetFinalizer(a, finalizer)
	return a
}

func NewAdapterWithDbName(driverName string, dataSourceName string, dbName string) *Adapter {
	a := &Adapter{}
	a.driverName = driverName
	a.dataSourceName = dataSourceName
	a.DbName = dbName
	// Open the DB, create it if not existed.
	a.open()
	// Call the destructor when the object is released.
	runtime.SetFinalizer(a, finalizer)
	return a
}

func (a *Adapter) CreateDatabase() error {
	db, err := dbx.Open(a.driverName, a.dataSourceName)
	if err != nil {
		return err
	}
	defer db.Close()
	var stmt string
	switch a.driverName {
	case "sqlite", "sqlite3":
		// SQLite has no CREATE DATABASE. The database IS the file, and it
		// exists the moment it is opened — there is nothing to create, so the
		// correct statement is no statement.
		//
		// Without this branch sqlite fell through to default, which emits
		// MySQL DDL, and every caller got `near "DATABASE": syntax error`. A
		// default that assumes one dialect is wrong for every dialect it has
		// not met; the ones that need a database created now say so by name.
		return nil
	case "postgres":
		stmt = fmt.Sprintf(`
			DO $$
			BEGIN
				IF NOT EXISTS (SELECT FROM pg_database WHERE datname = '%s') THEN
					CREATE DATABASE %s WITH ENCODING='UTF8' LC_COLLATE='en_US.utf8' LC_CTYPE='en_US.utf8' TEMPLATE=template0;
				END IF;
			END
			$$`,
			a.DbName, a.DbName)
	default:
		stmt = fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s default charset utf8mb4 COLLATE utf8mb4_general_ci", a.DbName)
	}
	_, err = db.NewQuery(stmt).Execute()
	if err != nil {
		return err
	}
	return nil
}

func (a *Adapter) open() {
	dataSourceName := a.dataSourceName + a.DbName
	if a.driverName != "mysql" {
		dataSourceName = a.dataSourceName
	}
	db, err := dbx.MustOpen(a.driverName, dataSourceName)
	if err != nil {
		panic(err)
	}
	a.db = db
}

func (a *Adapter) close() {
	a.db.Close()
	a.db = nil
}

func (a *Adapter) createTable() {
	// The Go structs are the single source of truth for the schema. dbx.Sync is
	// idempotent — CREATE TABLE IF NOT EXISTS for new tables and ALTER TABLE ADD
	// COLUMN for fields added since the table was created — so this is safe on
	// both a fresh embedded SQLite store and an existing DB. No external SQL.
	models := []interface{}{
		&Application{}, &Article{}, &Asset{}, &Chat{}, &Connection{},
		&File{}, &Form{}, &Graph{}, &Message{}, &ModelRoute{}, &Node{},
		&Provider{}, &Record{}, &Scale{}, &Scan{}, &Session{}, &Store{},
		&Task{}, &Template{}, &Vector{}, &Video{}, &Workflow{},
		&Memory{},             // cloud memory backend (per-user scoped)
		&OrgSettings{},        // per-org feature overrides (auto-routing, …)
		&RoutingEvent{},       // privacy-preserving auto-routing decision ledger (training)
		&RouterArtifactMeta{}, // latest retrain outcome per scope (upsert, one row/owner)
		&RouterTrainingLog{},  // append-only retrain timeline (one immutable row per fit)
		&FinetuneJob{},        // fine-tuning / training runs brokered to the cluster trainer
		&ModelAccess{},        // per-(org,user,model) grants for gated SKUs (enso limited preview)
	}
	for _, m := range models {
		if err := a.db.Sync(m); err != nil {
			// Log and continue so one bad model can't block the rest; a table
			// that genuinely failed to create will surface at first use.
			fmt.Printf("createTable: sync %T failed: %v\n", m, err)
		}
	}
	// dbx.Sync's ALTER TABLE ADD COLUMN carries no default, so rows that predate a
	// newly-added column hold SQL NULL there — which a full struct scan cannot read
	// into a Go scalar ("converting NULL to string is unsupported"). Repair the one
	// table whose per-org router/judge config the trainer reads at boot and on
	// cadence; idempotent, so it is a no-op once the rows are clean.
	backfillNullScalars(a.db, "org_settings", &OrgSettings{})
	// Routes are configuration — tens of rows, not millions — so the whole-model
	// sweep is the cheap option here rather than the expensive one, and every column
	// added to a route from now on is repaired without another line. Priced is the
	// one that needs it today: a bool cannot take NULL, so without this every read
	// of a route written before the column fails outright rather than reading false.
	backfillNullScalars(a.db, "model_route", &ModelRoute{})
	// The message table is the largest here and a whole-model sweep costs a scan per
	// column, so it is repaired one field at a time. These are the columns added since
	// message rows existed: a NULL claimed_time would make its row unclaimable — a
	// message nobody can answer — and a NULL in either fails the scan into a string
	// field before any handler sees the row.
	backfillNullField(a.db, "message", &Message{}, "ClaimedTime")
	backfillNullField(a.db, "message", &Message{}, "AnsweredTime")
}

// RawDB returns the underlying *sql.DB for direct access when needed.
func (a *Adapter) RawDB() *sql.DB {
	return a.db.DB()
}

// UseMemoryDB points the package adapter at a fresh in-memory SQLite database,
// syncs the given models, and returns a restore func that reinstates the previous
// adapter and closes the DB. It is the ONE hermetic in-memory-DB seam shared by
// every package's tests — object's own unit tests and the controllers' HTTP-status
// tests — so no test hand-rolls adapter wiring. SQLite is the canonical embedded
// store (the same driver prod uses), so this exercises the real query path, not a
// fake. Never call it from non-test code: it swaps the process-wide adapter.
func UseMemoryDB(dsn string, models ...interface{}) (restore func(), err error) {
	db, err := dbx.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		if err := db.Sync(m); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	prev := adapter
	adapter = &Adapter{driverName: "sqlite", dataSourceName: dsn, db: db}
	return func() { adapter = prev; _ = db.Close() }, nil
}
