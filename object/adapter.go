// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

	"github.com/beego/beego"
	_ "github.com/denisenkom/go-mssqldb" // mssql
	_ "github.com/go-sql-driver/mysql"   // mysql
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/dbx"
	_ "github.com/lib/pq"  // postgres
	_ "modernc.org/sqlite" // sqlite
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
	case "sqlite", "sqlite3":
		// SQLite has no server-side CREATE DATABASE; the file is created on
		// open via the DSN. dbx.Open above already opened (and thus created)
		// it, so there is nothing further to do.
		return nil
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

// createTable creates (or, for an existing schema, adds any missing columns to)
// every persistent model's table. dbx.Sync is the idempotent, cross-driver
// replacement for the xorm engine.Sync2 calls this code used before the
// xorm -> hanzoai/dbx migration (commit c3dc6ad8): it reads the same struct
// tags the CRUD layer uses (db: tags + snake_case fallback), so the columns it
// creates are exactly the ones adapter.db.Model/Select read and write. Schema
// is derived from the Go models — there is no separate SQL migration source.
//
// The model list mirrors the original engine.Sync2 sequence one-for-one; keep
// it in sync when a new persistent model is added.
func (a *Adapter) createTable() {
	models := []interface{}{
		&Video{}, &Store{}, &Provider{}, &File{}, &Vector{}, &Chat{}, &Message{},
		&Template{}, &Application{}, &Node{}, &Machine{}, &Image{}, &Container{},
		&Pod{}, &Task{}, &Scale{}, &Form{}, &Workflow{}, &Article{}, &Session{},
		&Connection{}, &Record{}, &Graph{}, &Hospital{}, &Doctor{}, &Patient{},
		&Caase{}, &Consultation{}, &Asset{}, &Scan{}, &ModelRoute{},
	}
	if err := a.db.Sync(models...); err != nil {
		panic(fmt.Errorf("createTable: dbx sync failed: %w", err))
	}
}

// RawDB returns the underlying *sql.DB for direct access when needed.
func (a *Adapter) RawDB() *sql.DB {
	return a.db.DB()
}
