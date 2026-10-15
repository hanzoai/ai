// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

// priced.go — the usage ledger as the rates job reads it: one row a call, each
// with what it was routed to, what it cost and what it billed (hanzo.cloud_usage).

import (
	"context"
	"fmt"
	"time"
)

// PricedRow is one call of a family as its row records it.
type PricedRow struct {
	ID        string
	At        time.Time
	Model     string
	Reasoning string
	// Served and Vendor are the arm that answered: its upstream and the provider
	// whose account ran it.
	Served string
	Vendor string
	// Status is "success" for an answer and "failover" for an arm that failed first.
	Status     string
	Prompt     int64 // every prompt token, fresh, cached and written
	Cached     int64
	Written    int64
	Completion int64
	CostNano   int64
	BilledNano int64
	LossNano   int64
	// Uncosted says nobody stated the call's cost, so CostNano is no fact.
	Uncosted bool
}

// pricedColumns are the columns a PricedRow reads, deduplicated by id the way
// every ledger read is (the table is a ReplacingMergeTree, merged in the
// background).
const pricedColumns = "SELECT id, any(timestamp) AS at, any(model) AS model, any(reasoning) AS reasoning, " +
	"any(served) AS served, any(vendor) AS vendor, any(status) AS status, any(prompt_tokens) AS prompt, " +
	"any(cache_read_tokens) AS cached, any(cache_write_tokens) AS written, any(completion_tokens) AS completion, " +
	"any(cost_nano) AS cost, any(billed_nano) AS billed, any(loss_nano) AS loss, any(uncosted) AS uncosted " +
	"FROM (SELECT * FROM hanzo.cloud_usage WHERE provider = ? AND timestamp >= ?) GROUP BY id "

// PricedRows reads a family's rows since since, newest first, at most limit.
func PricedRows(ctx context.Context, provider string, since time.Time, limit int) ([]PricedRow, error) {
	return pricedQuery(ctx, pricedColumns+"ORDER BY at DESC LIMIT ?", provider, since, limit)
}

// LastPricedRows reads, for each model and reasoning depth of a family, its last n
// rows since since, newest first.
func LastPricedRows(ctx context.Context, provider string, since time.Time, n int) ([]PricedRow, error) {
	return pricedQuery(ctx, pricedColumns+"ORDER BY at DESC LIMIT ? BY model, reasoning", provider, since, n)
}

func pricedQuery(ctx context.Context, q, provider string, since time.Time, n int) ([]PricedRow, error) {
	if !DatastoreEnabled() {
		return nil, fmt.Errorf("usage ledger unavailable: datastore peer not connected")
	}
	rows, err := DatastoreQuery(ctx, q, provider, since.UTC(), n)
	if err != nil {
		return nil, err
	}
	out := make([]PricedRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, pricedRow(r))
	}
	return out, nil
}

// pricedRow reads one result row, whatever native types the driver decoded.
func pricedRow(r map[string]any) PricedRow {
	return PricedRow{
		ID:         cuString(r["id"]),
		At:         cuTime(r["at"]),
		Model:      cuString(r["model"]),
		Reasoning:  cuString(r["reasoning"]),
		Served:     cuString(r["served"]),
		Vendor:     cuString(r["vendor"]),
		Status:     cuString(r["status"]),
		Prompt:     cuInt64(r["prompt"]),
		Cached:     cuInt64(r["cached"]),
		Written:    cuInt64(r["written"]),
		Completion: cuInt64(r["completion"]),
		CostNano:   cuInt64(r["cost"]),
		BilledNano: cuInt64(r["billed"]),
		LossNano:   cuInt64(r["loss"]),
		Uncosted:   cuBool(r["uncosted"]),
	}
}
