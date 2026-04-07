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
	"strings"

	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
	"github.com/sashabaranov/go-openai"
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
	if sortField == "" || sortOrder == "" {
		sortField = "created_time"
	}
	col := util.SnakeString(sortField)
	if sortOrder == "ascend" {
		q = q.OrderBy(col + " ASC")
	} else {
		q = q.OrderBy(col + " DESC")
	}
	if offset != -1 && limit != -1 {
		q = q.Offset(int64(offset)).Limit(int64(limit))
	}
	return q
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
