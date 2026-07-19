// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

// model_access.go — access gating for LISTED-but-limited-preview family SKUs.
//
// A gated SKU (enso) is discoverable by everyone (it appears in /v1/models with
// access:"waitlist") but callable only with a granted ModelAccess row. Enforcement is
// at ONE choke point — the family pipe — so every dialect (chat, messages, embeddings)
// is gated by the same rule. Users request access; a SuperAdmin grants it; the launch
// allowlist is seeded at boot (object.initModelAccessSeed). ai learns WHICH models are
// gated from discovery (zenModel.gated), never a hardcode.

import (
	"encoding/json"
	"fmt"

	"github.com/hanzoai/ai/object"
	iam "github.com/hanzoai/iam"
)

// modelAccessInfo is the additive /v1/models annotation for a gated SKU: the caller's
// standing. "waitlist" = gated and the caller has not requested; "requested" = pending;
// "granted" = callable. Omitted entirely for a generally-available model.
type modelAccessInfo struct {
	State string `json:"state"`
}

// principalAccessIdentity resolves the (org, key, email) a grant is matched on. The
// key prefers the email (human-recognizable, how an admin grants) and falls back to
// the username; enforcement matches on BOTH, so either keying works.
func principalAccessIdentity(u *iam.User) (owner, key, email string) {
	if u == nil {
		return "", "", ""
	}
	email = u.Email
	key = u.Email
	if key == "" {
		key = u.Name
	}
	return u.Owner, key, email
}

// gatedAccessMessage is the 403 body for a gated model the caller may not use — it
// tells the caller exactly how to request access.
func gatedAccessMessage(model string) string {
	return fmt.Sprintf("%s is in limited preview — request access: POST /v1/models/%s/access", model, model)
}

// modelAccessGrantedFn resolves whether a caller holds a grant for a gated SKU
// (default object.IsModelAccessGranted). Indirected so the shared gated-access rule
// is unit-testable without a live grants DB, mirroring routingEventSink/orgAutoRoutingLookup.
var modelAccessGrantedFn = object.IsModelAccessGranted

// grantAllows is the waitlist/grant half of family serve-access, shared by the serve
// pipe (familyAccessAllowed) and the auto-router's `known` predicate (modelServable):
// a SKU unknown to discovery or not gated always passes; a gated SKU (enso limited
// preview) requires a granted ModelAccess row for the caller (org-wide, username, or
// email). zm/ok is the caller's already-resolved discovery lookup — the two callers
// differ ONLY in how they resolve the SKU (the serving family vs. cross-family
// discovery), never in the grant decision. HIP-510 §1: `known` admits only models the
// caller can actually serve, and access-gated SKUs additionally require an explicit grant.
func grantAllows(zm zenModel, ok bool, orgId string, authUser *iam.User) bool {
	if !ok || !zm.gated() {
		return true
	}
	owner := orgId
	if owner == "" && authUser != nil {
		owner = authUser.Owner
	}
	name, email := "", ""
	if authUser != nil {
		name, email = authUser.Name, authUser.Email
	}
	return modelAccessGrantedFn(owner, name, email, zm.ID)
}

// familyAccessAllowed is the enforcement predicate at the family pipe: a family SKU is
// servable only when BOTH gates pass — the per-tier gate (Seam A: enso-flash free, enso
// trial+, enso-ultra paid; fail-safe on any commerce uncertainty) AND the waitlist/grant
// gate (grantAllows). Unknown-to-discovery is treated as non-gated (fail-open only for
// models the family does not advertise as gated).
func (c *ApiController) familyAccessAllowed(fam *modelFamily, model, orgId string, authUser *iam.User) bool {
	if !familyTierAllowed(familyAccessSubject(orgId, authUser), model) {
		return false
	}
	zm, ok := fam.lookup(model)
	return grantAllows(zm, ok, orgId, authUser)
}

// modelServable reports whether the caller can actually serve a model id — the SAME two
// gates the serve pipe (familyAccessAllowed) enforces, tier AND grant, resolved via
// cross-family discovery (familyLookupFresh, as FamilyModelGated does) since only the id
// is known. The auto-router folds this into its `known` predicate so `auto` never routes
// a caller to a family SKU their tier or grant would make the serve path refuse.
func modelServable(model, orgId string, authUser *iam.User) bool {
	if !familyTierAllowed(familyAccessSubject(orgId, authUser), model) {
		return false
	}
	zm, ok := familyLookupFresh(model)
	return grantAllows(zm, ok, orgId, authUser)
}

// FamilyModelGated reports whether a model id is a gated family SKU per discovery.
// Exported so the balance filter (routers) can ask without knowing the family layout.
// Access to a gated SKU still requires a grant (familyAccessAllowed), but a gated
// SKU is PAID like any other model — a grant unlocks access, never free billing.
func FamilyModelGated(model string) bool {
	zm, ok := familyLookupFresh(model)
	return ok && zm.gated()
}

// annotateModelAccess overlays the caller's access standing onto each gated SKU in a
// /v1/models listing. A gated SKU already carries the default "waitlist" state; this
// upgrades it to "requested"/"granted" for the authenticated caller.
func annotateModelAccess(models []modelInfo, user *iam.User) {
	if user == nil {
		return
	}
	owner, _, email := principalAccessIdentity(user)
	for i := range models {
		if models[i].Access == nil {
			continue // not gated
		}
		if st := object.ModelAccessStatus(owner, user.Name, email, models[i].ID); st != "" {
			models[i].Access = &modelAccessInfo{State: st}
		}
	}
}

// ── endpoints ────────────────────────────────────────────────────────────────

// RequestModelAccess (POST /v1/models/:model/access) records the caller's waitlist
// request for a gated model. Authed, idempotent, and self-scoped (the row is keyed to
// the caller's own org/identity — never a body-supplied owner). Returns the status.
func (c *ApiController) RequestModelAccess() {
	user := c.principalUser()
	if user == nil {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return
	}
	model := c.Ctx.Input.Param(":model")
	if model == "" {
		c.ResponseError("model is required")
		return
	}
	owner, key, email := principalAccessIdentity(user)
	row, err := object.RequestModelAccess(owner, key, email, model)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(row)
}

// GetModelAccessStatus (GET /v1/models/:model/access) returns the caller's standing
// for a gated model ("granted" | "requested" | "" none).
func (c *ApiController) GetModelAccessStatus() {
	user := c.principalUser()
	if user == nil {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return
	}
	model := c.Ctx.Input.Param(":model")
	owner, _, email := principalAccessIdentity(user)
	c.ResponseOk(map[string]string{
		"model":  model,
		"status": object.ModelAccessStatus(owner, user.Name, email, model),
	})
}

// AdminGrantModelAccess (POST /v1/admin/model-access) grants (or requests) access for
// any org/user/model. SuperAdmin only — mirrors the router_policy admin pattern but
// at platform scope. Body: {owner, user, email?, model, status?}. user "" grants the
// whole org; status defaults to "granted".
func (c *ApiController) AdminGrantModelAccess() {
	if !c.RequireSuperAdmin() {
		return
	}
	var body struct {
		Owner  string `json:"owner"`
		User   string `json:"user"`
		Email  string `json:"email"`
		Model  string `json:"model"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &body); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if body.Owner == "" || body.Model == "" {
		c.ResponseError("owner and model are required")
		return
	}
	status := body.Status
	if status == "" {
		status = object.ModelAccessGranted
	}
	row, err := object.UpsertModelAccess(body.Owner, body.User, body.Email, body.Model, status)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(row)
}

// AdminListModelAccess (GET /v1/admin/model-access[?owner=]) lists access rows.
// SuperAdmin only; without ?owner it returns every org's rows.
func (c *ApiController) AdminListModelAccess() {
	if !c.RequireSuperAdmin() {
		return
	}
	owner := c.Input().Get("owner")
	rows, err := object.ListModelAccess(owner)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(rows)
}
