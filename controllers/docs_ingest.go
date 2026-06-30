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

package controllers

import (
	"encoding/json"

	"github.com/hanzoai/ai/object"
)

// IngestDocs
// @Title IngestDocs
// @Tag Docs Ingest API
// @Description Unified RAG ingest: parse + chunk + embed documents and pipe them
//
//	to BOTH Hanzo Vector (semantic) AND Hanzo Search (keyword) under the tenant
//	index {owner}-{store}-docs — the same index /v1/chat retrieval reads. The
//	source is pluggable: "upload" (inline files/documents), "github" (index a
//	repo), "crawl" (web), or "s3" (the store's object-storage space). The owner
//	is bound to the authenticated principal; the client-supplied owner is never
//	trusted.
//
// @Param body body object.IngestRequest true "Ingest request"
// @Success 200 {object} object.IngestStats "Ingest statistics"
// @router /docs/ingest [post]
func (c *ApiController) IngestDocs() {
	auth := c.requireIndexAuth()
	if auth == nil {
		return
	}

	var req object.IngestRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Gate external/bulk sources (github/crawl/s3) on balance, mirroring the
	// scrape gate. Pure inline "upload" is ungated, matching /v1/index.
	if auth.Owner != "" && req.Source != "" && req.Source != "upload" {
		balance, balErr := getUserBalance(object.BillingSubjectFromUserKey(auth.Owner, auth.UserID), auth.Owner)
		if balErr == nil && balance <= 0 {
			c.ResponseError("insufficient balance for ingest operation. Add funds at https://hanzo.ai/billing")
			return
		}
	}

	stats, err := object.IngestSource(auth.Owner, &req, c.GetAcceptLanguage())
	if err != nil {
		recordSearchUsage(auth, "index-docs", req.Source, "error", 0, c.Ctx.Request.RemoteAddr)
		c.ResponseError(err.Error())
		return
	}

	recordSearchUsage(auth, "index-docs", req.Source, "success", stats.DocumentsIndexed, c.Ctx.Request.RemoteAddr)

	// Purge Cloudflare edge cache for this index so retrieval doesn't serve
	// stale results after ingest. Runs async to avoid blocking the response.
	go purgeCFCacheTag("search:" + stats.IndexName)

	c.ResponseOk(stats)
}
