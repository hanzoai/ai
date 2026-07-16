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

	"github.com/hanzoai/beego"
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
	err := beego.LoadAppConfig("ini", "../conf/app.conf")
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
		&Application{}, &Article{}, &Asset{}, &Caase{}, &Chat{}, &Connection{},
		&Consultation{}, &Container{}, &Doctor{}, &File{}, &Form{}, &Graph{},
		&Hospital{}, &Image{}, &Machine{}, &Message{}, &ModelRoute{}, &Node{},
		&Patient{}, &Pod{}, &Provider{}, &Record{}, &Scale{}, &Scan{},
		&Session{}, &Store{}, &Task{}, &Template{}, &Vector{}, &Video{},
		&Workflow{},
		&Memory{},       // cloud memory backend (per-user scoped)
		&OrgSettings{},  // per-org feature overrides (auto-routing, …)
		&RoutingEvent{}, // privacy-preserving auto-routing decision ledger (training)
		&FinetuneJob{},  // fine-tuning / training runs brokered to the cluster trainer
		&ModelAccess{},  // per-(org,user,model) grants for gated SKUs (enso limited preview)
	}
	for _, m := range models {
		if err := a.db.Sync(m); err != nil {
			// Log and continue so one bad model can't block the rest; a table
			// that genuinely failed to create will surface at first use.
			fmt.Printf("createTable: sync %T failed: %v\n", m, err)
		}
	}
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
