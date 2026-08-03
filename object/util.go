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
	"fmt"
	"net/url"
	"strings"

	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
	"github.com/hanzoai/go-openai"
)

func getUrlFromPath(path string, origin string) (string, error) {
	if strings.HasPrefix(path, "http") {
		return path, nil
	}
	res := strings.Replace(path, ":", "|", 1)
	res = fmt.Sprintf("storage/%s", res)
	res, err := url.JoinPath(origin, res)
	return res, err
}

// GetDbQuery builds a SelectQuery with pagination, filtering, and sorting.
// This replaces the old GetDbSession which returned an xorm.Session.
func GetDbQuery(owner string, offset, limit int, field, value, sortField, sortOrder string) *dbx.SelectQuery {
	q := adapter.db.Select()
	if owner != "" {
		q = q.AndWhere(dbx.HashExp{"owner": owner})
	}
	if field != "" && value != "" {
		if util.FilterField(field) {
			col := util.SnakeString(field)
			q = q.AndWhere(dbx.Like(col, value))
		}
	}
	q = q.OrderBy(sortColumn(sortField, sortOrder) + sortDirection(sortOrder))
	if offset != -1 && limit != -1 {
		q = q.Offset(int64(offset)).Limit(int64(limit))
	}
	return q
}

// sortColumn maps a caller's sortField to the column that goes into ORDER BY.
// It is the ONE place that mapping happens, because it is the whitelist: the
// value is caller-supplied and lands in an IDENTIFIER position, where a bound
// parameter cannot protect it, so nothing but a whitelist can. It is the same
// util.FilterField the filter column above uses, for the same reason.
//
// The quoting underneath does not protect it. dbx.QuoteColumnName returns its
// argument UNQUOTED when it contains "(", "{{" or "[[", so a value carrying a
// parenthesis is concatenated into ORDER BY verbatim. And SnakeString does not
// sanitize — it only lowercases and inserts "_" before capitals — so an
// ALL-LOWERCASE payload passes through it byte for byte. Measured against the
// real builder on the real driver, "(select group_concat(name) from
// sqlite_master)" reached ORDER BY whole and the statement executed;
// "iif((select …)>2,name,owner)" survives too, which is the conditional a blind
// read needs to walk a column one bit at a time. A table prefix is no defence
// either: a comma opens a second ORDER BY term, so "name,(select …)" lands
// after a "chat." prefix just as well. Every paginated list endpoint takes this
// parameter.
//
// A rejected value falls back to the default rather than erroring: the sort
// order is presentation, and no caller sending a real column is affected — the
// UI sends Ant Design dataIndex names ("createdTime", "displayName"), which are
// alphanumeric and pass. The default is a literal, so it is returned AFTER the
// check and never has to satisfy it (it contains "_", which the whitelist
// deliberately excludes).
func sortColumn(sortField, sortOrder string) string {
	if sortField == "" || sortOrder == "" || !util.FilterField(sortField) {
		return "created_time"
	}
	return util.SnakeString(sortField)
}

// sortDirection is the binary the UI sends; anything that is not "ascend"
// descends. No caller text reaches the SQL through it.
func sortDirection(sortOrder string) string {
	if sortOrder == "ascend" {
		return " ASC"
	}
	return " DESC"
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	retryableErrors := []string{
		string(openai.RunErrorRateLimitExceeded),
	}
	for _, retryableErr := range retryableErrors {
		if strings.Contains(err.Error(), retryableErr) {
			return true
		}
	}
	return false
}
