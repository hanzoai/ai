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

package util

import (
	"math"
	"net/http"
	"strconv"
)

// Paginator turns the "p" page-number query parameter plus a page size and a
// known total count into the offset for an offset-based list query. It reads
// the same "p" parameter and computes the same offset as the historical
// list handlers.
type Paginator struct {
	request *http.Request
	perPage int
	nums    int64
	page    int
}

// NewPaginator builds a Paginator over a page size and total count. A per of
// zero or less defaults to 10.
func NewPaginator(req *http.Request, per int, nums int64) *Paginator {
	if per <= 0 {
		per = 10
	}
	return &Paginator{request: req, perPage: per, nums: nums}
}

// Nums returns the total item count.
func (p *Paginator) Nums() int64 { return p.nums }

// Offset is the item offset of the requested page: (page - 1) * perPage.
func (p *Paginator) Offset() int { return (p.currentPage() - 1) * p.perPage }

// pageCount is the number of pages the count spans at the page size.
func (p *Paginator) pageCount() int {
	return int(math.Ceil(float64(p.nums) / float64(p.perPage)))
}

// currentPage reads the requested page from the "p" query parameter and
// clamps it into [1, pageCount], caching the result.
func (p *Paginator) currentPage() int {
	if p.page != 0 {
		return p.page
	}
	if p.request.Form == nil {
		p.request.ParseForm()
	}
	page, _ := strconv.Atoi(p.request.Form.Get("p"))
	if n := p.pageCount(); page > n {
		page = n
	}
	if page <= 0 {
		page = 1
	}
	p.page = page
	return p.page
}
