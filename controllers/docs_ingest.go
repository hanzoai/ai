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
	"errors"

	"github.com/hanzoai/account"

	"github.com/hanzoai/ai/log"

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
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Gate external/bulk sources (github/crawl/s3) on balance, mirroring the
	// scrape gate. Pure inline "upload" is ungated, matching /v1/index.
	if auth.Owner != "" && req.Source != "" && req.Source != "upload" {
		balance, balErr := getUserBalance(account.PayerOf(auth.Owner, auth.UserID).Subject(), auth.Owner)
		if balErr == nil && balance <= 0 {
			c.ResponseError("insufficient balance for ingest operation. Add funds at https://hanzo.ai/billing")
			return
		}
	}

	lang := c.GetAcceptLanguage()

	// A long source (github repo / crawl / s3) is past the sync budget — enqueue it as
	// a durable hanzoai/tasks workflow (the ONE async system) and return the workflow
	// id immediately; the caller tracks it in the Tasks product. Durability is a bonus,
	// never a dependency: if the engine is unwired (TASKS_ADDR unset) OR any enqueue
	// fails, fall through to running inline so ingest ALWAYS works.
	if object.IsAsyncIngestSource(req.Source) {
		wfID, err := object.EnqueueIngest(c.Context(), auth.Owner, &req, lang)
		if err == nil {
			recordSearchUsage(auth, "index-docs", req.Source, "enqueued", 0, c.Fiber().IP())
			c.ResponseOk(&object.IngestStats{
				Source:     req.Source,
				Store:      req.Store,
				IndexName:  object.GetSearchIndexName(auth.Owner, req.Store),
				Async:      true,
				WorkflowID: wfID,
			})
			return
		}
		if !errors.Is(err, object.ErrTasksNotConfigured) {
			// Engine wired but this enqueue failed — don't break ingest; log and inline.
			log.Warning("ingest: durable enqueue failed, running inline: %s", err.Error())
		}
		// fall through to inline ingest
	}

	stats, err := object.IngestSource(auth.Owner, &req, lang)
	if err != nil {
		recordSearchUsage(auth, "index-docs", req.Source, "error", 0, c.Fiber().IP())
		c.ResponseError(err.Error())
		return
	}

	recordSearchUsage(auth, "index-docs", req.Source, "success", stats.DocumentsIndexed, c.Fiber().IP())

	// Purge Cloudflare edge cache for this index so retrieval doesn't serve
	// stale results after ingest. Runs async to avoid blocking the response.
	go purgeCFCacheTag("search:" + stats.IndexName)

	c.ResponseOk(stats)
}
