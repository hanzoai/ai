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
	"fmt"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/hanzoai/dbx"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx: the driver the host runs hanzo-sql on
)

// TestSQLEnv names a PostgreSQL server (a postgres:// URL, a role that may create
// databases) that UseMemoryDB runs every test on in place of SQLite when it is set.
// The suite is one body run on both engines, so a statement that only SQLite reads
// fails on the server it will run on.
const TestSQLEnv = "AI_TEST_SQL_DSN"

var (
	testDatabases atomic.Int64
	testRun       = time.Now().UnixNano()
)

// postgresDB opens a fresh database on the server at dsn, as the connection a host
// hands ai (pgx), and returns the func that drops it.
func postgresDB(dsn string) (*dbx.DB, func(), error) {
	base, err := dbx.Open("pgx", dsn)
	if err != nil {
		return nil, nil, err
	}
	name := fmt.Sprintf("ai_test_%d_%d", testRun, testDatabases.Add(1))
	if _, err := base.NewQuery("CREATE DATABASE " + quote(name)).Execute(); err != nil {
		_ = base.Close()
		return nil, nil, err
	}
	drop := func() {
		_, _ = base.NewQuery("DROP DATABASE IF EXISTS " + quote(name) + " WITH (FORCE)").Execute()
		_ = base.Close()
	}
	u, err := url.Parse(dsn)
	if err != nil {
		drop()
		return nil, nil, err
	}
	u.Path = "/" + name
	db, err := dbx.Open("pgx", u.String())
	if err != nil {
		drop()
		return nil, nil, err
	}
	return db, func() { _ = db.Close(); drop() }, nil
}
