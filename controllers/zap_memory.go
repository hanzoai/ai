// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// Native ZAP handlers for the /v1/memory/* route group (strangler migration of
// controllers/memory.go). Pure ZAP: no beego controller, no http writer. The
// logic is re-implemented against object/ + the shared ZAP auth seam, mirroring
// controllers.ApiController's Memory* methods EXACTLY:
//
//   - identity is resolved from the auth token via zapResolveUser (the ONE auth
//     seam), NEVER from the request body. "owner/name" splits to (org, userID),
//     the same pair the beego path derives from the gateway-minted X-Org-Id
//     (JWT owner) / X-User-Id (JWT sub) headers.
//   - every storage call is scoped by (org, userID); applyMemoryIdentity force-
//     sets identity so a body-supplied owner/userId can never be honored.
//   - memory is NOT an LLM path: the beego controller runs no balance gate and
//     records no usage, so neither does this handler (behavior parity — there is
//     no meter to extend and no gate to enforce for a memory read/write).
//   - success returns the beego Response{status:"ok",data} envelope verbatim via
//     the shared zapOk; failures use the shared zapErr (the sibling group's
//     established convention for this migration).
//
// Registration reuses the per-group registry defined by the sibling group in
// zap_rag-search-crawl.go (registerCloud / registerGatewayPath). This file only
// self-registers the memory group's methods + path prefixes from its own init();
// the dispatcher wiring (handleCloudService / handleGatewayHTTPRequest consulting
// LookupZapCloudHandler / LookupZapGatewayHandler) and the beego-route flip in
// router.go are the Integrate step. Until then beego keeps serving /v1/memory/*
// in parallel (strangler hybrid).
//
// READ-PARAM NOTE: read endpoints (search/list/recall/facts) take q/kind/limit.
// The registry handler signature carries only (ctx, auth, body), so these are
// read from the JSON body — the native-ZAP shape. The gateway (MsgType 200) GET
// surface must therefore have Integrate fold the HTTP query string into the body
// it hands the handler (a generic one-liner in the dispatcher it owns), or callers
// pass the params as a JSON body. Org scoping + envelope are unaffected.

package controllers

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

func init() {
	registerZapMemory()
}

// registerZapMemory wires the memory group's native cloud methods (MsgType 100)
// and gateway path prefixes (MsgType 200) into the shared registry. Named so
// InitZapHandlers can also invoke it explicitly.
func registerZapMemory() {
	registerCloud("memory.remember", zapMemoryRememberHandler)
	registerCloud("memory.search", zapMemorySearchHandler)
	registerCloud("memory.list", zapMemoryListHandler)
	registerCloud("memory.recall", zapMemoryRecallHandler)
	registerCloud("memory.facts", zapMemoryFactsHandler)
	registerCloud("memory.update", zapMemoryUpdateHandler)
	registerCloud("memory.delete", zapMemoryDeleteHandler)

	// Longest-prefix wins in LookupZapGatewayHandler, so the seven exact paths
	// each resolve to their own handler.
	registerGatewayPath("/v1/memory/remember", zapMemoryRememberHandler)
	registerGatewayPath("/v1/memory/search", zapMemorySearchHandler)
	registerGatewayPath("/v1/memory/list", zapMemoryListHandler)
	registerGatewayPath("/v1/memory/recall", zapMemoryRecallHandler)
	registerGatewayPath("/v1/memory/facts", zapMemoryFactsHandler)
	registerGatewayPath("/v1/memory/update", zapMemoryUpdateHandler)
	registerGatewayPath("/v1/memory/delete", zapMemoryDeleteHandler)
}

// ── Identity (the ONE auth seam) ─────────────────────────────────────────

// zapMemoryIdentity resolves the trusted (org, userID) from the auth token.
// zapResolveUser routes pk-/sk- IAM keys through getUserByAccessKey and JWTs
// through object.ParseAndValidateJWT (sig + iss/aud), returning "owner/name":
// org = owner (== gateway X-Org-Id / JWT owner), userID = name (== X-User-Id /
// JWT sub). Empty auth or a missing half → 401, matching requireMemoryIdentity.
func zapMemoryIdentity(auth string) (org, userID string, err error) {
	id, resolveErr := zapResolveUser(auth)
	if resolveErr != nil {
		return "", "", resolveErr
	}
	org = id
	if i := strings.IndexByte(id, '/'); i >= 0 {
		org = id[:i]
		userID = id[i+1:]
	}
	if strings.TrimSpace(org) == "" || strings.TrimSpace(userID) == "" {
		return "", "", authError("Please sign in first")
	}
	return org, userID, nil
}

// memReadParams is the read-endpoint parameter set (search/list/recall/facts).
// Native cloud sends it as the JSON body; the gateway GET surface folds its query
// string into this shape (see READ-PARAM NOTE above).
type memReadParams struct {
	Q     string `json:"q"`
	Kind  string `json:"kind"`
	Limit int    `json:"limit"`
}

func decodeReadParams(body []byte) memReadParams {
	var p memReadParams
	_ = json.Unmarshal(body, &p)
	p.Q = strings.TrimSpace(p.Q)
	return p
}

func decodeMemoryRequest(body []byte) (memoryRequest, error) {
	var req memoryRequest
	if len(body) == 0 {
		return req, nil
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, err
	}
	return req, nil
}

// memoryLimitOr clamps an already-parsed int limit to def on non-positive input,
// mirroring memoryLimit's semantics (empty/invalid/≤0 → def).
func memoryLimitOr(raw, def int) int {
	if raw <= 0 {
		return def
	}
	return raw
}

// ── Handlers ─────────────────────────────────────────────────────────────

func zapMemoryRememberHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, userID, err := zapMemoryIdentity(auth)
	if err != nil {
		return zapErr(statusOf(err), err.Error())
	}
	req, err := decodeMemoryRequest(body)
	if err != nil {
		return zapErr(400, err.Error())
	}
	if strings.TrimSpace(req.Content) == "" {
		return zapErr(400, "The memory content should not be empty")
	}

	memory := &object.Memory{Content: req.Content, Kind: req.Kind, Metadata: req.Metadata}
	applyMemoryIdentity(memory, org, userID)
	object.EmbedMemory(memory, "en")

	affected, err := object.AddMemory(memory)
	if err != nil {
		return zapErr(500, err.Error())
	}
	if !affected {
		return zapErr(500, "Failed to store the memory")
	}
	return zapOk(memory)
}

func zapMemorySearchHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, userID, err := zapMemoryIdentity(auth)
	if err != nil {
		return zapErr(statusOf(err), err.Error())
	}
	p := decodeReadParams(body)
	if p.Q == "" {
		return zapErr(400, "The search query should not be empty")
	}
	results, err := object.SearchMemories(org, userID, p.Q, p.Kind, memoryLimitOr(p.Limit, 20), "en")
	if err != nil {
		return zapErr(500, err.Error())
	}
	return zapOk(results)
}

func zapMemoryListHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, userID, err := zapMemoryIdentity(auth)
	if err != nil {
		return zapErr(statusOf(err), err.Error())
	}
	p := decodeReadParams(body)
	memories, err := object.RecallMemories(org, userID, p.Kind, memoryLimitOr(p.Limit, 100))
	if err != nil {
		return zapErr(500, err.Error())
	}
	return zapOk(memories)
}

func zapMemoryRecallHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, userID, err := zapMemoryIdentity(auth)
	if err != nil {
		return zapErr(statusOf(err), err.Error())
	}
	p := decodeReadParams(body)
	limit := memoryLimitOr(p.Limit, 20)
	if p.Q != "" {
		results, err := object.SearchMemories(org, userID, p.Q, p.Kind, limit, "en")
		if err != nil {
			return zapErr(500, err.Error())
		}
		return zapOk(results)
	}
	memories, err := object.RecallMemories(org, userID, p.Kind, limit)
	if err != nil {
		return zapErr(500, err.Error())
	}
	return zapOk(memories)
}

func zapMemoryFactsHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, userID, err := zapMemoryIdentity(auth)
	if err != nil {
		return zapErr(statusOf(err), err.Error())
	}
	p := decodeReadParams(body)
	facts, err := object.GetFacts(org, userID, memoryLimitOr(p.Limit, 100))
	if err != nil {
		return zapErr(500, err.Error())
	}
	return zapOk(facts)
}

func zapMemoryUpdateHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, userID, err := zapMemoryIdentity(auth)
	if err != nil {
		return zapErr(statusOf(err), err.Error())
	}
	req, err := decodeMemoryRequest(body)
	if err != nil {
		return zapErr(400, err.Error())
	}
	if req.target() == "" {
		return zapErr(400, "The memory id should not be empty")
	}

	existing, err := object.GetMemoryByIdScoped(org, userID, req.target())
	if err != nil {
		return zapErr(500, err.Error())
	}
	if existing == nil {
		return zapErr(404, "The memory was not found")
	}

	patch := &object.Memory{Content: req.Content, Kind: req.Kind, Metadata: req.Metadata}
	affected, err := object.UpdateMemoryScoped(org, userID, existing.Name, patch, "en")
	if err != nil {
		return zapErr(500, err.Error())
	}
	return zapOk(affected)
}

func zapMemoryDeleteHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	org, userID, err := zapMemoryIdentity(auth)
	if err != nil {
		return zapErr(statusOf(err), err.Error())
	}
	req, err := decodeMemoryRequest(body)
	if err != nil {
		return zapErr(400, err.Error())
	}
	if req.target() == "" {
		return zapErr(400, "The memory id should not be empty")
	}

	existing, err := object.GetMemoryByIdScoped(org, userID, req.target())
	if err != nil {
		return zapErr(500, err.Error())
	}
	if existing == nil {
		return zapErr(404, "The memory was not found")
	}

	affected, err := object.DeleteMemoryScoped(org, userID, existing.Name)
	if err != nil {
		return zapErr(500, err.Error())
	}
	return zapOk(affected)
}
