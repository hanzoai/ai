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

// Direct Datastore datastore client — the OLAP ledger transport.
//
// This is the ONE way the cloud runtime reaches the analytics datastore
// (Datastore, deployed as `datastore` / `insights-datastore` in-cluster). It
// backs the hanzo.cloud_usage usage ledger (console2 Overview + admin god-view)
// and the hanzo.observations trace ledger.
//
// WHY NOT ZAP. The prior design routed these through a ZAP "datastore peer"
// (see object/zap.go), which required (a) object.InitZap() — a node the UNIFIED
// cloud binary deliberately never starts (its ZAP listener lives only in
// cmd/aid), and (b) a ZAP server on the datastore's :9999 — which does not
// exist (the datastore image is Datastore behind nginx on :8123/:9000, no ZAP
// bridge). So the peer never connected, DatastoreEnabled() was always false,
// and BOTH the read (get-cloud-usages) and write (zapWriteUsage/zapWriteTrace)
// paths were dead. The datastore speaks Datastore's own protocol; we speak it
// directly — the same hanzo-ds/go recipe cloud's audit OLAP mirror and the
// insights/o11y stack already uses. No sidecar, no bridge, one transport.
//
// InitDatastore is called from the SHARED Bootstrap (bootstrap.go), so it runs
// identically in the standalone (cmd/aid) and the embedded unified cloud binary
// with no boot-order or listener coupling. It is opt-in: with no DATASTORE_ADDR
// the ledger stays honest-empty (DatastoreEnabled() == false) rather than
// fabricating zeros.
package object

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/beego/logs"
	datastore "github.com/hanzo-ds/go"
	"github.com/hanzo-ds/go/lib/driver"
)

// ── Package state ───────────────────────────────────────────────────────
var (
	datastoreConn  driver.Conn
	datastoreMu    sync.RWMutex
	datastoreReady atomic.Bool
)

// ── Initialization ──────────────────────────────────────────────────────

// InitDatastore opens the Datastore connection that backs the usage +
// observability ledgers. Opt-in via DATASTORE_ADDR (host:port of the native
// port, default 9000). Non-fatal and asynchronous: a transient datastore
// outage at boot must never take down the cloud process, so it retries in the
// background and only latches ready once a Ping succeeds. Until then
// DatastoreEnabled() reports false and the ledger surfaces an honest
// "unavailable" instead of blocking boot.
//
// Config (all from env / KMS-injected secrets, never hard-coded):
//
//	DATASTORE_ADDR      host:9000 of the datastore native port (enables the ledger)
//	DATASTORE_DB        database (default "hanzo")
//	DATASTORE_USER      user     (default "default")
//	DATASTORE_PASSWORD  password (KMS-backed secret)
func InitDatastore() {
	addr := strings.TrimSpace(os.Getenv("DATASTORE_ADDR"))
	if addr == "" {
		logs.Info("datastore: disabled (set DATASTORE_ADDR to enable the Datastore usage/observability ledger)")
		return
	}
	go connectDatastore(addr)
}

// connectDatastore dials Datastore with bounded retry/backoff and latches the
// connection on the first successful Ping. datastore-go's own pool reconnects
// transparently after that, so this only needs to win once.
func connectDatastore(addr string) {
	db := datastoreEnv("DATASTORE_DB", "hanzo")
	user := datastoreEnv("DATASTORE_USER", "default")
	pass := os.Getenv("DATASTORE_PASSWORD")

	for attempt := 1; attempt <= 30; attempt++ {
		conn, err := datastore.Open(&datastore.Options{
			Addr:        []string{addr},
			Auth:        datastore.Auth{Database: db, Username: user, Password: pass},
			DialTimeout: 5 * time.Second,
		})
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = conn.Ping(ctx)
			cancel()
			if err == nil {
				// Self-provision the ledger database. The hanzo.cloud_usage /
				// hanzo.observations DDL (Ensure*Table) qualifies by database, so the
				// database must exist first. CREATE DATABASE is not scoped to the
				// connection's default db, so this works even though Auth.Database
				// names a db that may not exist yet. Non-fatal: a warn (not a hard
				// fail) keeps connect resilient when the db already exists but the
				// user lacks CREATE DATABASE — Ensure*Table is then the authority.
				ddctx, ddcancel := context.WithTimeout(context.Background(), 5*time.Second)
				if derr := conn.Exec(ddctx, "CREATE DATABASE IF NOT EXISTS "+db); derr != nil {
					logs.Warn("datastore: ensure database %q: %v", db, derr)
				}
				ddcancel()

				datastoreMu.Lock()
				datastoreConn = conn
				datastoreMu.Unlock()
				datastoreReady.Store(true)
				logs.Info("datastore: connected to Datastore at %s (db=%s)", addr, db)
				return
			}
			_ = conn.Close()
		}
		logs.Warn("datastore: connect %s attempt %d: %v", addr, attempt, err)
		time.Sleep(2 * time.Second)
	}
	logs.Error("datastore: failed to connect to %s after 30 attempts", addr)
}

// DatastoreEnabled reports whether the Datastore ledger connection is live. The
// read path gates on it to return an honest "unavailable" (not fake zeros) and
// the write path gates on it to skip the insert when the warehouse is absent.
func DatastoreEnabled() bool { return datastoreReady.Load() }

// ── Exec / Query ────────────────────────────────────────────────────────

// DatastoreExec runs a DDL or INSERT against Datastore. `?` placeholders are
// bound positionally from args (datastore-go renders them into the statement).
func DatastoreExec(ctx context.Context, stmt string, args ...interface{}) error {
	datastoreMu.RLock()
	conn := datastoreConn
	datastoreMu.RUnlock()
	if conn == nil {
		return fmt.Errorf("datastore: not connected")
	}
	return conn.Exec(ctx, stmt, args...)
}

// DatastoreQuery runs a SELECT and returns rows as column→value maps, decoding
// each column into its native Datastore scan type (uint64, string, time.Time,
// float64, …). The cloud_usage read layer's cu* coercers accept those native
// types, so callers never touch reflect. Symmetric to DatastoreExec.
func DatastoreQuery(ctx context.Context, query string, args ...interface{}) ([]map[string]interface{}, error) {
	datastoreMu.RLock()
	conn := datastoreConn
	datastoreMu.RUnlock()
	if conn == nil {
		return nil, fmt.Errorf("datastore: not connected")
	}
	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := rows.Columns()
	types := rows.ColumnTypes()
	out := make([]map[string]interface{}, 0, 16)
	for rows.Next() {
		dest := make([]interface{}, len(cols))
		for i := range dest {
			dest[i] = reflect.New(types[i].ScanType()).Interface() // *T for the column's native type
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("datastore scan: %w", err)
		}
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			m[c] = reflect.ValueOf(dest[i]).Elem().Interface()
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("datastore rows: %w", err)
	}
	return out, nil
}

func datastoreEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
