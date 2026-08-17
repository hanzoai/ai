// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// retrievalOwner returns the IAM org whose search index should be queried. The
// tenant is bound to the AUTHENTICATED principal — the user's own org, or for a
// public widget the org bound to the widget KEY. It is NEVER derived from the
// request Origin/Referer, which a client can forge to read another tenant's RAG
// store (cross-tenant disclosure).
func retrievalOwner(authUser *iam.User, token string) string {
	if authUser != nil && authUser.Owner != "" {
		return authUser.Owner
	}
	if isWidgetKey(token) {
		return widgetKeyOwner(token)
	}
	return ""
}

// widgetKeyOwner resolves a widget key (hz_*) to its bound IAM org via the ONE
// canonical resolver (object.WidgetKeyOwner) shared with the router balance gate,
// so a widget key means the same org for both tenant isolation and billing. See
// object/widget_owner.go for the resolution order (WIDGET_KEY_OWNERS →
// WIDGET_DEFAULT_OWNER, KMS-or-env). Empty ⇒ unattributable ⇒ callers fail secure.
func widgetKeyOwner(token string) string {
	return object.WidgetKeyOwner(token)
}

// retrievalFlags is the retrieval ask as the BODY carries it. A browser
// preflights custom headers and the edge's CORS allow-list names only the
// standard ones, so a public page cannot send X-Retrieval — the body is how it
// asks. The header form stays for server-side callers; either spelling works.
type retrievalFlags struct {
	Retrieval bool   `json:"retrieval"`
	Store     string `json:"retrieval_store"`
}

func (c *ApiController) bodyRetrieval() retrievalFlags {
	var f retrievalFlags
	_ = json.Unmarshal(c.Ctx.Input.RequestBody, &f)
	return f
}

// retrievalStore names the store to search: the header if present, else the
// body, else empty — which retrieveKnowledgeIfEnabled resolves to the default.
func (c *ApiController) retrievalStore() string {
	if v := c.Ctx.Request.Header.Get("X-Retrieval-Store"); v != "" {
		return v
	}
	return c.bodyRetrieval().Store
}

// retrievalEnabled decides whether to augment the prompt with retrieved docs.
func (c *ApiController) retrievalEnabled(token string) bool {
	if v := c.Header("X-Retrieval"); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	if c.Header("X-Retrieval-Store") != "" {
		return true
	}
	if f := c.bodyRetrieval(); f.Retrieval || f.Store != "" {
		return true
	}
	if isWidgetKey(token) && strings.EqualFold(os.Getenv("WIDGET_RETRIEVAL"), "1") {
		return true
	}
	return false
}

// retrieveKnowledgeIfEnabled pulls top-K relevant documents from the owner's
// search store. Returns an empty slice on any failure so the LLM call still
// proceeds without RAG.
func (c *ApiController) retrieveKnowledgeIfEnabled(
	question, owner, store, lang string,
) []*model.RawMessage {
	empty := []*model.RawMessage{}
	token := bearerTokenFromRequest(c.Ctx.Request)
	if !c.retrievalEnabled(token) {
		return empty
	}
	if owner == "" {
		return empty
	}
	if store == "" {
		store = c.Input().Get("store")
	}
	if store == "" {
		// Brand-neutral per-org default (white-label): the owner prefix on the index
		// (`{owner}-{store}-docs`) is what isolates each org, so the store slug itself
		// must NOT bake in a brand. Every org's assistant reads its OWN docs store.
		store = object.DefaultDocsStore
	}

	req := &object.DocSearchRequest{Query: question, Limit: 4}
	hits, err := object.SearchDocuments(owner, store, req, lang)
	if err != nil {
		log.Warning("chat retrieval: search %s/%s failed: %s", owner, store, err.Error())
		return empty
	}
	out := make([]*model.RawMessage, 0, len(hits))
	for _, h := range hits {
		if h.Content == "" {
			continue
		}
		out = append(out, &model.RawMessage{Author: "Knowledge", Text: h.Content})
	}
	return out
}

func bearerTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	// First-party cookie fallback: a browser cookie session carries no
	// Authorization header, but Signin persisted the verified IAM access token as
	// the hanzo_iam_token cookie so the stateless validator can re-derive identity
	// when the in-memory the router session is gone (self-heal). See iamTokenCookieName.
	if ck, err := r.Cookie(iamTokenCookieName); err == nil && ck != nil && ck.Value != "" {
		return ck.Value
	}
	return ""
}
