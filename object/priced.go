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

// priced.go — the usage ledger as the price report reads it: a family's calls,
// grouped by the model asked, the arm that answered and its provider
// (hanzo.cloud_usage).

import (
	"context"
	"fmt"
	"time"
)

// Routed is a family's calls over a window that share a model, the arm that answered
// (or failed), its provider and a status: how many, and what they cost and billed,
// nano-dollars.
type Routed struct {
	Model string
	// Served and Vendor are the arm: its upstream and the provider whose account ran
	// it.
	Served string
	Vendor string
	// Status is "success" for an answer and "failover" for an arm that failed first.
	Status   string
	Requests int64
	// Uncosted is how many stated no cost: their cost is no fact.
	Uncosted int64
	// Losses is how many cost us more than they billed.
	Losses     int64
	CostNano   int64
	BilledNano int64
	LossNano   int64
	// KnownNano is what the calls whose cost is known billed: the margin is it less
	// CostNano.
	KnownNano int64
}

// routedQuery groups a family's rows since a time, each deduplicated by id first (the
// table is a ReplacingMergeTree, merged in the background) and filtered in its own
// scan below the dedup. The dedup's columns are named apart from the aggregates'
// aliases, which ClickHouse would otherwise read inside the aggregates.
const routedQuery = "SELECT model, served, vendor, status, count() AS requests, sum(u) AS uncosted, " +
	"countIf(l > 0) AS losses, sum(c) AS cost, sum(b) AS billed, sum(l) AS loss, sumIf(b, u = 0) AS known " +
	"FROM (SELECT id, any(model) AS model, any(served) AS served, any(vendor) AS vendor, any(status) AS status, " +
	"any(cost_nano) AS c, any(billed_nano) AS b, any(loss_nano) AS l, any(uncosted) AS u " +
	"FROM (SELECT * FROM hanzo.cloud_usage WHERE provider = ? AND timestamp >= ?) GROUP BY id) " +
	"GROUP BY model, served, vendor, status"

// RoutedRows reads a family's calls since since, grouped.
func RoutedRows(ctx context.Context, provider string, since time.Time) ([]Routed, error) {
	if !DatastoreEnabled() {
		return nil, fmt.Errorf("usage ledger unavailable: datastore peer not connected")
	}
	rows, err := DatastoreQuery(ctx, routedQuery, provider, since.UTC())
	if err != nil {
		return nil, err
	}
	out := make([]Routed, 0, len(rows))
	for _, r := range rows {
		out = append(out, routed(r))
	}
	return out, nil
}

// routed reads one result row, whatever native types the driver decoded.
func routed(r map[string]any) Routed {
	return Routed{
		Model:      cuString(r["model"]),
		Served:     cuString(r["served"]),
		Vendor:     cuString(r["vendor"]),
		Status:     cuString(r["status"]),
		Requests:   cuInt64(r["requests"]),
		Uncosted:   cuInt64(r["uncosted"]),
		Losses:     cuInt64(r["losses"]),
		CostNano:   cuInt64(r["cost"]),
		BilledNano: cuInt64(r["billed"]),
		LossNano:   cuInt64(r["loss"]),
		KnownNano:  cuInt64(r["known"]),
	}
}
