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

// The hanzo.observations trace ledger — one GENERATION row per inference call,
// written by controllers/zap_native.go zapWriteTrace. This is the per-tenant
// trace/observation half of the cloud o11y pipeline (the usage/spend half is
// hanzo.cloud_usage). The row shape is the canonical Langfuse-style observation
// (trace_id, GENERATION, token counts, per-leg cost, tags, level) with an
// organization column promoted out of the metadata so the ledger is per-tenant
// queryable. Same MergeTree discipline as cloud_usage: insert-only, 2y TTL.
package object

import (
	"context"
	"sync/atomic"
)

// observationsTableDDL is the ONE definition of the hanzo.observations schema.
// Column order matches the INSERT in zapWriteTrace exactly.
const observationsTableDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.observations (
		id String,
		trace_id String,
		name String,
		start_time DateTime,
		end_time DateTime,
		type LowCardinality(String),
		model String,
		organization String,
		prompt_tokens UInt32,
		completion_tokens UInt32,
		total_tokens UInt32,
		input_cost Float64,
		output_cost Float64,
		total_cost Float64,
		metadata String,
		tags String,
		user_id String,
		session_id String,
		level LowCardinality(String),
		status_message String,
		version String
	) ENGINE = MergeTree()
	ORDER BY (start_time, organization, user_id)
	TTL start_time + INTERVAL 2 YEAR`

var observationsTableReady atomic.Bool

// EnsureObservationsTable creates hanzo.observations if it does not exist. It is
// idempotent and only latches success, so a transient datastore outage at boot
// does not permanently poison later attempts (mirrors EnsureCloudUsageTable).
func EnsureObservationsTable(ctx context.Context) error {
	if observationsTableReady.Load() {
		return nil
	}
	if err := DatastoreExec(ctx, observationsTableDDL); err != nil {
		return err
	}
	observationsTableReady.Store(true)
	return nil
}
