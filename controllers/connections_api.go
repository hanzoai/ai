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

// AI login-manager: a curated, org-scoped surface over object.Provider that lets
// a logged-in org connect its OWN third-party AI accounts (OpenAI / Anthropic /
// Google) by supplying an API key. The key is sealed into KMS (never the DB, never
// a log line) exactly like the /v1/add-provider BYOK path; the stored provider row
// carries only a "kms://…" reference. Once connected, the org's completions run
// against that account (BYO) and are metered with the 1% platform fee dimension
// (see platform_fee.go / recordUsage) instead of Hanzo credits.
//
// Design mirrors cloud_usage.go: the HTTP handlers are thin orchestration; the
// mapping + view shaping are pure functions the tests drive directly, so the two
// hard invariants (the raw key is never persisted/echoed; the response never leaks
// the key or its kms ref) are provable without a live KMS or datastore.

package controllers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// aiConnSpec maps a consumer-provider slug to the object.Provider shape the row is
// stored as. Type strings are the canonical ones the completion paths already key
// on: "OpenAI" (openai_api.go), "Claude" (anthropic_api.go), "Gemini"
// (embeddings_api.go / openai_api.go). Category is always "Model", matching the
// seeded provider rows in object.initLLMProviders.
type aiConnSpec struct {
	name        string // object.Provider.Name (also the public slug)
	typ         string // object.Provider.Type
	subType     string // default upstream model (overwritten per-request at routing)
	providerURL string // upstream base URL
	label       string // human display name
}

// aiConnSpecs is the closed allow-list of connectable consumer providers. Groq is
// OpenAI-compatible so its row is typed "OpenAI" pointed at Groq's base URL.
var aiConnSpecs = map[string]aiConnSpec{
	"openai":     {name: "openai", typ: "OpenAI", subType: "gpt-5", providerURL: "https://api.openai.com/v1", label: "OpenAI"},
	"anthropic":  {name: "anthropic", typ: "Claude", subType: "claude-opus-4-8", providerURL: "https://api.anthropic.com", label: "Anthropic"},
	"google":     {name: "google", typ: "Gemini", subType: "gemini-2.5-pro", providerURL: "https://generativelanguage.googleapis.com/v1beta/openai", label: "Google Gemini"},
	"openrouter": {name: "openrouter", typ: "OpenRouter", subType: "openrouter/auto", providerURL: "https://openrouter.ai/api/v1", label: "OpenRouter"},
	"deepseek":   {name: "deepseek", typ: "DeepSeek", subType: "deepseek-chat", providerURL: "https://api.deepseek.com/v1", label: "DeepSeek"},
	"groq":       {name: "groq", typ: "OpenAI", subType: "llama-3.3-70b-versatile", providerURL: "https://api.groq.com/openai/v1", label: "Groq"},
}

// aiConnOrder fixes the listing order so GetAIConnections is deterministic.
var aiConnOrder = []string{"openai", "anthropic", "google", "openrouter", "deepseek", "groq"}

// aiConnSpecFor resolves a provider slug to its spec, case-insensitively. ok is
// false for any provider outside the allow-list.
func aiConnSpecFor(provider string) (aiConnSpec, bool) {
	spec, ok := aiConnSpecs[strings.ToLower(strings.TrimSpace(provider))]
	return spec, ok
}

// aiConnProviderList is the human list of connectable slugs for the "unknown provider"
// error, derived from the ONE registry order so it never drifts from aiConnSpecs.
func aiConnProviderList() string { return strings.Join(aiConnOrder, ", ") }

// aiConnResponse is one connection's PUBLIC shape. It carries a boolean key signal
// only — never the key value and never the "kms://…" reference.
type aiConnResponse struct {
	Provider     string `json:"provider"`
	Connected    bool   `json:"connected"`
	AccountLabel string `json:"account_label"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// connectionSecretName is the deterministic, per-org KMS secret name for a
// connection. It reuses the ONE BYOK sealing convention already used by
// AddProvider (controllers/provider.go byokSecretName) so a connection and a
// hand-added BYOK provider of the same (org, name) seal to the same secret — one
// way to name a tenant key.
func connectionSecretName(org, provider string) string {
	return byokSecretName(org, provider)
}

// buildConnectionProvider assembles the org's object.Provider row from a SEALED
// secret reference. SEAL INVARIANT: secretRef is ALWAYS a "kms://…" reference (or
// empty), never a raw key — this function is the only constructor of a connection
// row and it copies secretRef verbatim into ClientSecret, so a raw key can never
// reach the row. Pure.
func buildConnectionProvider(org string, spec aiConnSpec, secretRef string) *object.Provider {
	return &object.Provider{
		Owner:        org,
		Name:         spec.name,
		DisplayName:  spec.label,
		Category:     "Model",
		Type:         spec.typ,
		SubType:      spec.subType,
		ProviderUrl:  spec.providerURL,
		ClientSecret: secretRef, // kms:// ref only — enforced by the caller (StoreProviderSecret)
		State:        "Active",
		CreatedTime:  util.GetCurrentTime(),
	}
}

// connectionView renders one connection's public status from the org's (possibly
// nil) row. connected is true only for an Active row whose key resolves present
// (object.ProviderKeyPresent never returns the value); a missing or deactivated
// (disconnected) row is not connected. NO-LEAK INVARIANT: the returned struct
// carries no secret material — account_label is "<org>/<name>", not the key. Pure.
func connectionView(provider string, row *object.Provider) aiConnResponse {
	resp := aiConnResponse{Provider: provider}
	if object.ModelProviderUsable(row) && object.ProviderKeyPresent(row) {
		resp.Connected = true
		resp.AccountLabel = row.Owner + "/" + row.Name
		resp.UpdatedAt = row.CreatedTime
	}
	return resp
}

// requireConnectionOrg resolves the caller's org from the VERIFIED principal
// (session or signed JWT) and rejects an anonymous request. Connections are
// org-scoped and self-served — a normal logged-in org user manages THEIR OWN org's
// connections (not a super-admin operation). GetOrg binds any X-Org-Id
// to the principal's own org (a non-admin can never target another org).
func (c *ApiController) requireConnectionOrg() (string, bool) {
	user := c.principalUser()
	if user == nil || strings.TrimSpace(user.Owner) == "" {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return "", false
	}
	org := c.GetOrg()
	if strings.TrimSpace(org) == "" {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return "", false
	}
	return org, true
}

// GetAIConnections lists the org's connectable AI accounts and whether each is
// currently connected. Never returns a key or a kms:// reference.
// @Title GetAIConnections
// @Tag AI Connections API
// @Description list the org's connected third-party AI accounts (login manager)
// @Success 200 {array} controllers.aiConnResponse The Response object
// @router /ai/connections [get]
func (c *ApiController) GetAIConnections() {
	org, ok := c.requireConnectionOrg()
	if !ok {
		return
	}
	out := make([]aiConnResponse, 0, len(aiConnOrder))
	for _, p := range aiConnOrder {
		spec := aiConnSpecs[p]
		row, err := object.GetProvider(org + "/" + spec.name)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		out = append(out, connectionView(p, row))
	}
	c.ResponseOk(out)
}

// AddAIConnection connects (or reconnects) a third-party AI account for the org by
// sealing the supplied key into KMS and upserting the org's provider row. The raw
// key is sealed BEFORE the row is built and is never persisted or echoed.
// @Title AddAIConnection
// @Tag AI Connections API
// @Description connect a third-party AI account by API key (sealed into KMS)
// @Param body body object true "{provider, apiKey}"
// @Success 200 {object} controllers.aiConnResponse The Response object
// @router /ai/connections [post]
func (c *ApiController) AddAIConnection() {
	org, ok := c.requireConnectionOrg()
	if !ok {
		return
	}
	var body struct {
		Provider string `json:"provider"`
		APIKey   string `json:"apiKey"`
	}
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &body); err != nil {
		c.ResponseError(err.Error())
		return
	}
	spec, ok := aiConnSpecFor(body.Provider)
	if !ok {
		c.ResponseError(c.T("openai:provider must be one of") + ": " + aiConnProviderList())
		return
	}
	if strings.TrimSpace(body.APIKey) == "" {
		c.ResponseError(c.T("openai:apiKey is required"))
		return
	}

	// Seal the raw key into KMS FIRST — fails closed (sealProviderSecret refuses to
	// store plaintext when KMS is unconfigured), so the row is never built or written
	// with a raw key. sealProviderSecret + persistConnection are the SAME seal +
	// upsert path the OAuth connect flow (CallbackAIProvider) uses.
	ref, err := sealProviderSecret(connectionSecretName(org, spec.name), body.APIKey)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, "secret store unavailable, cannot store key")
		return
	}
	if err := persistConnection(org, spec, ref); err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(aiConnResponse{Provider: spec.name, Connected: true, AccountLabel: org + "/" + spec.name})
}

// DeleteAIConnection disconnects a third-party AI account: it deactivates the org's
// row so completion resolution falls back to the global Hanzo account (no BYO), and
// best-effort tombstones the sealed secret. Idempotent.
// @Title DeleteAIConnection
// @Tag AI Connections API
// @Description disconnect a third-party AI account
// @Param provider path string true "provider slug"
// @Success 200 {object} controllers.aiConnResponse The Response object
// @router /ai/connections/:provider [delete]
func (c *ApiController) DeleteAIConnection() {
	org, ok := c.requireConnectionOrg()
	if !ok {
		return
	}
	spec, ok := aiConnSpecFor(c.Ctx.Input.Param(":provider"))
	if !ok {
		c.ResponseError(c.T("openai:provider must be one of") + ": " + aiConnProviderList())
		return
	}
	id := org + "/" + spec.name
	existing, err := object.GetProvider(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if existing == nil {
		// Nothing connected — already disconnected.
		c.ResponseOk(aiConnResponse{Provider: spec.name, Connected: false})
		return
	}

	// Deactivate so ModelProviderUsable is false and the org reverts to the global
	// built-in on the next completion. "Disabled" is the inactive State the repo
	// uses (object.initLLMProviders / ModelProviderUsable).
	existing.State = "Disabled"
	if _, err := object.UpdateProvider(id, existing); err != nil {
		c.ResponseError(err.Error())
		return
	}
	// Best-effort tombstone the sealed secret value. The deactivation above is the
	// authoritative disconnect; a KMS failure here must not fail the request.
	_, _ = object.StoreProviderSecret(connectionSecretName(org, spec.name), "")
	object.InvalidateProviderNameCache(spec.name)

	c.ResponseOk(aiConnResponse{Provider: spec.name, Connected: false})
}
