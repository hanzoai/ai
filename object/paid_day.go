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

import (
	"github.com/hanzoai/dbx"
)

// PaidDay is what one org's calls on the paid lane spent in one UTC day, in
// nano-dollars (controllers' lane.go). It is kept in this process's store so a restart
// or a rollout keeps the platform's paid-lane day and each org's share of it.
type PaidDay struct {
	Day   string `db:"pk" json:"day"` // the UTC day, 2006-01-02
	Org   string `db:"pk" json:"org"` // the org whose ledger paid
	Nanos int64  `json:"nanos"`
}

// PaidDays reads what each org's paid-lane calls spent on day. No store answers
// nothing: the day is this process's alone.
func PaidDays(day string) (map[string]int64, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	var rows []PaidDay
	if err := adapter.db.Select().From("paid_day").Where(dbx.HashExp{"day": day}).All(&rows); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Org] += r.Nanos
	}
	return out, nil
}

// CountPaidDay adds nanos to what org's paid-lane calls spent on day. The caller makes
// one call at a time (controllers' laneBook), so the row an update misses is inserted
// by exactly one.
func CountPaidDay(day, org string, nanos int64) error {
	if adapter == nil || adapter.db == nil || nanos == 0 {
		return nil
	}
	res, err := adapter.db.NewQuery("UPDATE {{paid_day}} SET [[nanos]] = [[nanos]] + {:n} WHERE [[day]] = {:day} AND [[org]] = {:org}").
		Bind(dbx.Params{"n": nanos, "day": day, "org": org}).Execute()
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	return insertRow(adapter.db, &PaidDay{Day: day, Org: org, Nanos: nanos})
}

// DropPaidDays deletes every day before day: a day's count is read only while it is
// the current one.
func DropPaidDays(before string) error {
	if adapter == nil || adapter.db == nil {
		return nil
	}
	_, err := adapter.db.NewQuery("DELETE FROM {{paid_day}} WHERE [[day]] < {:day}").Bind(dbx.Params{"day": before}).Execute()
	return err
}
