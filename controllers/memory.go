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

// Memory controller — the cloud /v1/ai/memory/* surface of the unified memory
// interface. Mirrors the hanzo-mcp memory tool's actions (remember, search,
// list, recall, update, delete, facts) so the local on-disk backend and this
// cloud backend are interchangeable behind one interface.
//
// SECURITY: org + userId are resolved ONLY from the credential this process
// verified — never from a header, never from the request body. Every storage call
// is scoped by (org, userId), and that pair is what decides whose memories a
// request touches, so nothing a caller can write may contribute to it. A gateway
// does mint identity headers from a validated token, but a gateway is not the only
// way to reach this controller, and on any other route those headers are strings
// the caller chose.

package controllers

import (
	"encoding/json"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// memoryRequest is the POST body for remember/update/delete. It deliberately
// carries NO owner/userId field: identity is never accepted from the body.
type memoryRequest struct {
	Id       string                `json:"id"`   // owner/name (for update/delete addressing)
	Name     string                `json:"name"` // bare name (alternative addressing)
	Content  string                `json:"content"`
	Kind     string                `json:"kind"`
	Metadata object.MemoryMetadata `json:"metadata"`
}

// target returns the addressing key for update/delete (id preferred over name).
func (r *memoryRequest) target() string {
	if strings.TrimSpace(r.Id) != "" {
		return strings.TrimSpace(r.Id)
	}
	return strings.TrimSpace(r.Name)
}

// applyMemoryIdentity force-sets owner/userId from the authenticated identity,
// overwriting anything an attacker may have placed on the struct. This is the
// single chokepoint that guarantees a stored memory belongs to the caller.
func applyMemoryIdentity(memory *object.Memory, org, userID string) {
	memory.Owner = org
	memory.UserId = userID
}

// memoryUserID is the caller's user id: the name on the VERIFIED principal, which
// is what the session path and the native ZAP twin (zapMemoryIdentity) both key
// on, so one person's memories are one set however they arrive.
//
// IT IS NOT READ FROM A HEADER. X-User-Id is minted by a gateway from a validated
// token — true right up until this controller is reachable without passing through
// one, and then it is a string the caller chose. This id decides WHOSE memories are
// read, written and deleted, so the only thing allowed to answer is a credential
// this process verified itself.
func (c *ApiController) memoryUserID() string {
	if user := c.principalUser(); user != nil {
		if name := strings.TrimSpace(user.Name); name != "" {
			return name
		}
	}
	return c.GetSessionUsername()
}

// requireMemoryIdentity returns the caller's own (org, userId) or writes an auth
// error and returns ok=false.
//
// The org comes from GetOrg, which resolves it from the verified principal and
// never from a client header — the same tenant every other surface acts in. Both
// halves must be present: a request that names no person and no tenant is not
// anonymous here, it is unauthenticated, and memory is per-person by construction.
func (c *ApiController) requireMemoryIdentity() (string, string, bool) {
	org := c.GetOrg()
	userID := c.memoryUserID()
	if org == "" || userID == "" {
		c.ResponseError(c.T("auth:Please sign in first"))
		return "", "", false
	}
	return org, userID, true
}

// memoryLimit parses a client-supplied limit, falling back to def on empty,
// non-numeric, or non-positive input. Uses ParseIntWithError because ParseInt
// panics on bad input — a query param must never crash the handler.
func memoryLimit(raw string, def int) int {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	n, err := util.ParseIntWithError(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// MemoryRemember
// @Title MemoryRemember
// @Tag Memory API
// @Description store a memory for the authenticated user
// @Param body body controllers.memoryRequest true "content, kind, metadata"
// @Success 200 {object} object.Memory The Response object
// @router /memory/remember [post]
func (c *ApiController) MemoryRemember() {
	org, userID, ok := c.requireMemoryIdentity()
	if !ok {
		return
	}
	var req memoryRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		c.ResponseError(c.T("memory:The memory content should not be empty"))
		return
	}

	memory := &object.Memory{
		Content:  req.Content,
		Kind:     req.Kind,
		Metadata: req.Metadata,
	}
	applyMemoryIdentity(memory, org, userID)
	object.EmbedMemory(memory, c.GetAcceptLanguage())

	affected, err := object.AddMemory(memory)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if !affected {
		c.ResponseError(c.T("memory:Failed to store the memory"))
		return
	}
	c.ResponseOk(memory)
}

// MemorySearch
// @Title MemorySearch
// @Tag Memory API
// @Description search the authenticated user's memories (semantic, text fallback)
// @Param q     query string true  "search query"
// @Param kind  query string false "filter by kind"
// @Param limit query string false "max results"
// @Success 200 {array} object.Memory The Response object
// @router /memory/search [get]
func (c *ApiController) MemorySearch() {
	org, userID, ok := c.requireMemoryIdentity()
	if !ok {
		return
	}
	query := strings.TrimSpace(c.Input().Get("q"))
	if query == "" {
		c.ResponseError(c.T("memory:The search query should not be empty"))
		return
	}
	kind := c.Input().Get("kind")
	limit := memoryLimit(c.Input().Get("limit"), 20)

	results, err := object.SearchMemories(org, userID, query, kind, limit, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(results)
}

// MemoryList
// @Title MemoryList
// @Tag Memory API
// @Description list the authenticated user's memories, newest first
// @Param kind  query string false "filter by kind"
// @Param limit query string false "max results"
// @Success 200 {array} object.Memory The Response object
// @router /memory/list [get]
func (c *ApiController) MemoryList() {
	org, userID, ok := c.requireMemoryIdentity()
	if !ok {
		return
	}
	kind := c.Input().Get("kind")
	limit := memoryLimit(c.Input().Get("limit"), 100)

	memories, err := object.RecallMemories(org, userID, kind, limit)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(memories)
}

// MemoryRecall
// @Title MemoryRecall
// @Tag Memory API
// @Description recall recent/relevant memories for context injection; with q it
// @Description ranks semantically, without q it returns the most recent
// @Param q     query string false "optional relevance query"
// @Param kind  query string false "filter by kind"
// @Param limit query string false "max results"
// @Success 200 {array} object.Memory The Response object
// @router /memory/recall [get]
func (c *ApiController) MemoryRecall() {
	org, userID, ok := c.requireMemoryIdentity()
	if !ok {
		return
	}
	query := strings.TrimSpace(c.Input().Get("q"))
	kind := c.Input().Get("kind")
	limit := memoryLimit(c.Input().Get("limit"), 20)

	if query != "" {
		results, err := object.SearchMemories(org, userID, query, kind, limit, c.GetAcceptLanguage())
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		c.ResponseOk(results)
		return
	}

	memories, err := object.RecallMemories(org, userID, kind, limit)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(memories)
}

// MemoryFacts
// @Title MemoryFacts
// @Tag Memory API
// @Description list the authenticated user's stored facts
// @Param limit query string false "max results"
// @Success 200 {array} object.Memory The Response object
// @router /memory/facts [get]
func (c *ApiController) MemoryFacts() {
	org, userID, ok := c.requireMemoryIdentity()
	if !ok {
		return
	}
	limit := memoryLimit(c.Input().Get("limit"), 100)

	facts, err := object.GetFacts(org, userID, limit)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(facts)
}

// MemoryUpdate
// @Title MemoryUpdate
// @Tag Memory API
// @Description update one of the authenticated user's memories
// @Param body body controllers.memoryRequest true "id/name + fields to change"
// @Success 200 {object} controllers.Response The Response object
// @router /memory/update [post]
func (c *ApiController) MemoryUpdate() {
	org, userID, ok := c.requireMemoryIdentity()
	if !ok {
		return
	}
	var req memoryRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if req.target() == "" {
		c.ResponseError(c.T("memory:The memory id should not be empty"))
		return
	}

	existing, err := object.GetMemoryByIdScoped(org, userID, req.target())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if existing == nil {
		c.ResponseError(c.T("memory:The memory was not found"))
		return
	}

	patch := &object.Memory{Content: req.Content, Kind: req.Kind, Metadata: req.Metadata}
	affected, err := object.UpdateMemoryScoped(org, userID, existing.Name, patch, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(affected)
}

// MemoryDelete
// @Title MemoryDelete
// @Tag Memory API
// @Description delete one of the authenticated user's memories
// @Param body body controllers.memoryRequest true "id/name to delete"
// @Success 200 {object} controllers.Response The Response object
// @router /memory/delete [post]
func (c *ApiController) MemoryDelete() {
	org, userID, ok := c.requireMemoryIdentity()
	if !ok {
		return
	}
	var req memoryRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if req.target() == "" {
		c.ResponseError(c.T("memory:The memory id should not be empty"))
		return
	}

	existing, err := object.GetMemoryByIdScoped(org, userID, req.target())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if existing == nil {
		c.ResponseError(c.T("memory:The memory was not found"))
		return
	}

	affected, err := object.DeleteMemoryScoped(org, userID, existing.Name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(affected)
}
