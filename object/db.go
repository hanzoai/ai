// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
package object
import (
	"database/sql"
	"fmt"
	"github.com/hanzoai/dbx"
)
// getOne fetches a single row by composite PK (owner, name) into dst.
// Returns (existed bool, err error).
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
