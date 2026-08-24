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
	"fmt"

	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
)

// getOne fetches a single row by composite PK (owner, name) into dst.
// Returns (existed bool, err error).
// getRow reads the one row a table holds for an (owner, name), or nil when it
// holds none.
//
// Nine tables had this written out for them — the same ten lines with the type
// and the table name changed — so a correction to how a missing row is told from
// a failed read had nine places to be made and nine chances to be made in eight.
// The store guard is one of those corrections: reading through a nil adapter is a
// SIGSEGV rather than an error, and it belongs here rather than nine times.
func getRow[T any](table, owner, name string) (*T, error) {
	if adapter == nil || adapter.db == nil {
		return nil, fmt.Errorf("%s store is not initialised", table)
	}
	var row T
	existed, err := getOne(adapter.db, table, &row, pk2(owner, name))
	if err != nil {
		return &row, err
	}
	if existed {
		return &row, nil
	}
	return nil, nil
}

func getOne(db *dbx.DB, table string, dst interface{}, pk dbx.HashExp) (bool, error) {
	err := db.Select().From(table).Where(pk).One(dst)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// narrow adds each named column to a filter, skipping the ones left empty. Empty
// means unconstrained: these columns are always populated on a stored row, so
// matching the empty string would match nothing — which is never what a caller
// means by leaving a value out.
func narrow(where dbx.HashExp, optional map[string]string) dbx.HashExp {
	for col, v := range optional {
		if v != "" {
			where[col] = v
		}
	}
	return where
}

// findAll fetches all rows from table matching the where clause into dst slice.
func findAll(db *dbx.DB, table string, dst interface{}, where dbx.Expression, orderBy ...string) error {
	q := db.Select().From(table)
	if where != nil {
		q = q.Where(where)
	}
	for _, o := range orderBy {
		q = q.AndOrderBy(o)
	}
	return q.All(dst)
}

// insertRow inserts a struct model.
// updated writes a row back over the key it carries.
func updated(row any) (bool, error) {
	if adapter == nil || adapter.db == nil {
		return false, fmt.Errorf("store is not initialised")
	}
	if err := adapter.db.Model(row).Update(); err != nil {
		return false, err
	}
	return true, nil
}

// deleteRow removes the one row a table holds for an (owner, name) and says
// whether there was one to remove.
func deleteRow(table, owner, name string) (bool, error) {
	if adapter == nil || adapter.db == nil {
		return false, fmt.Errorf("%s store is not initialised", table)
	}
	affected, err := deleteByPK(adapter.db, table, pk2(owner, name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

// rowCount counts what a table holds for one owner under a filter.
//
// The -1, -1 is "no page" — GetDbQuery reads a non-negative offset and limit as a
// window and anything else as the whole set — and it was spelled out at
// twenty-three call sites, along with the two empty sort fields a count has no
// use for. A convention repeated is a convention that can be misremembered once.
func rowCount(table, owner, field, value string) (int64, error) {
	if adapter == nil || adapter.db == nil {
		return 0, fmt.Errorf("%s store is not initialised", table)
	}
	return queryCount(GetDbQuery(owner, -1, -1, field, value, "", ""), table)
}

// rowsPage lists one page of a table — the ordering and filtering the query
// carries, applied to the rows one owner holds.
func rowsPage[T any](table, owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*T, error) {
	if adapter == nil || adapter.db == nil {
		return nil, fmt.Errorf("%s store is not initialised", table)
	}
	rows := []*T{}
	if err := queryFind(GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder), table, &rows); err != nil {
		return rows, err
	}
	return rows, nil
}

// allRows lists every row a table holds, across owners, grouped by owner and
// newest first within each.
func rowsWhere[T any](table string, where dbx.Expression) ([]*T, error) {
	if adapter == nil || adapter.db == nil {
		return nil, fmt.Errorf("%s store is not initialised", table)
	}
	rows := []*T{}
	if err := findAll(adapter.db, table, &rows, where, "owner ASC", "created_time DESC"); err != nil {
		return rows, err
	}
	return rows, nil
}

func allRows[T any](table string) ([]*T, error) {
	return rowsWhere[T](table, nil)
}

// rowsOf lists what a table holds for one owner, newest first.
func rowsOf[T any](table, owner string) ([]*T, error) {
	if adapter == nil || adapter.db == nil {
		return nil, fmt.Errorf("%s store is not initialised", table)
	}
	rows := []*T{}
	if err := findAll(adapter.db, table, &rows, dbx.HashExp{"owner": owner}, "created_time DESC"); err != nil {
		return rows, err
	}
	return rows, nil
}

// rowAt reads the row an id names — the same read as getRow, reached by the
// joined form the API speaks in rather than by the pair.
func rowAt[T any](table, id string) (*T, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getRow[T](table, owner, name)
}

// addRow inserts one row and says whether it landed.
//
// Twelve tables had this written out, and each copy computed the answer through a
// counter: set to one, zeroed on the error the function had already returned on,
// then compared to zero. The counter could not hold any other value, so the
// comparison could not have any other outcome — five lines of arithmetic to say
// what "the insert did not fail" says.
func addRow(row any) (bool, error) {
	if adapter == nil || adapter.db == nil {
		return false, fmt.Errorf("store is not initialised")
	}
	if err := insertRow(adapter.db, row); err != nil {
		return false, err
	}
	return true, nil
}

func insertRow(db *dbx.DB, model interface{}) error {
	return db.Model(model).Insert()
}

// insertRows inserts multiple rows using a transaction.
func insertRows(db *dbx.DB, models ...interface{}) (int64, error) {
	var count int64
	err := db.Transactional(func(tx *dbx.Tx) error {
		for _, m := range models {
			if err := tx.Model(m).Insert(); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}

// updateByPK updates all columns for a row identified by composite PK.
func updateByPK(db *dbx.DB, table string, pk dbx.HashExp, cols dbx.Params) (int64, error) {
	result, err := db.Update(table, cols, pk).Execute()
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// updateCols updates specific columns for rows matching where.
func updateCols(db *dbx.DB, table string, where dbx.Expression, cols dbx.Params) (int64, error) {
	result, err := db.Update(table, cols, where).Execute()
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// deleteByPK deletes a row by composite PK.
func deleteByPK(db *dbx.DB, table string, pk dbx.HashExp) (int64, error) {
	result, err := db.Delete(table, pk).Execute()
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// deleteWhere deletes rows matching a where clause.
func deleteWhere(db *dbx.DB, table string, where dbx.Expression) (int64, error) {
	result, err := db.Delete(table, where).Execute()
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// countWhere counts rows in a table matching the where clause.
func countWhere(db *dbx.DB, table string, where dbx.Expression) (int64, error) {
	q := db.Select("COUNT(*)").From(table)
	if where != nil {
		q = q.Where(where)
	}
	var count int64
	err := q.Row(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// structToParams converts a struct's exported fields to dbx.Params using the field mapper.
// This is used for UPDATE operations where we need all column values.
func structToParams(db *dbx.DB, model interface{}) dbx.Params {
	// Use the model query's internal column extraction via a builder roundtrip.
	// For updates we construct params manually in each call site.
	_ = db
	_ = model
	return nil
}

// queryCount returns the count for a GetDbQuery-style query on a specific table.
func queryCount(q *dbx.SelectQuery, table string) (int64, error) {
	// Override selects and from for counting.
	info := q.Info()
	cq := info.Builder.Select("COUNT(*)").From(table)
	if info.Where != nil {
		cq = cq.Where(info.Where)
	}
	var count int64
	err := cq.Build().Row(&count)
	return count, err
}

// queryFind executes a GetDbQuery-style query on a specific table.
func queryFind(q *dbx.SelectQuery, table string, dst interface{}) error {
	info := q.Info()
	fq := info.Builder.Select().From(table)
	if info.Where != nil {
		fq = fq.Where(info.Where)
	}
	for _, o := range info.OrderBy {
		fq = fq.AndOrderBy(o)
	}
	if info.Limit >= 0 {
		fq = fq.Limit(info.Limit)
	}
	if info.Offset > 0 {
		fq = fq.Offset(info.Offset)
	}
	return fq.All(dst)
}

// pk2 is a convenience for composite (owner, name) primary keys.
func pk2(owner, name string) dbx.HashExp {
	return dbx.HashExp{"owner": owner, "name": name}
}

// pkID is a convenience for integer primary keys.
func pkID(id int) dbx.HashExp {
	return dbx.HashExp{"id": id}
}

// tableName returns the table name for a model type, matching dbx convention.
func tableName(name string) string {
	return fmt.Sprintf("{%s}", name) // dbx will not auto-quote table names in braces
}

// toInterfaceSlice converts a []string to []interface{} for use with dbx.In().
func toInterfaceSlice(s []string) []interface{} {
	result := make([]interface{}, len(s))
	for i, v := range s {
		result[i] = v
	}
	return result
}
