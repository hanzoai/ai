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

	"github.com/beego/beego/logs"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	iam "github.com/hanzoai/iam"
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

// widgetKeyOwner resolves a widget key (hz_*) to its bound IAM org. The owner is
// taken from the widget KEY — a per-tenant credential — via the WIDGET_KEY_OWNERS
// config (KMS first, env fallback) mapping key->owner. An unmapped key falls back
// to WIDGET_DEFAULT_OWNER (a single configured tenant), NEVER a header-derived
// org. This removes the Origin/Referer trust that allowed cross-tenant RAG reads.
func widgetKeyOwner(token string) string {
	if o, ok := loadWidgetKeyOwners()[token]; ok && o != "" {
		return o
	}
	return strings.TrimSpace(os.Getenv("WIDGET_DEFAULT_OWNER"))
}

// loadWidgetKeyOwners parses WIDGET_KEY_OWNERS (env or KMS) into a key->owner
// map. Accepts a JSON object or comma-separated key=value pairs.
func loadWidgetKeyOwners() map[string]string {
	raw := os.Getenv("WIDGET_KEY_OWNERS")
	if raw == "" {
		if v, err := object.GetKMSSecret("WIDGET_KEY_OWNERS"); err == nil {
			raw = v
		}
	}
	out := map[string]string{}
	if raw == "" {
		return out
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		_ = json.Unmarshal([]byte(raw), &out)
		return out
	}
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return out
}

// retrievalEnabled decides whether to augment the prompt with retrieved docs.
func (c *ApiController) retrievalEnabled(token string) bool {
	if v := c.Ctx.Request.Header.Get("X-Retrieval"); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	if c.Ctx.Request.Header.Get("X-Retrieval-Store") != "" {
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
		store = "docs-hanzo-ai"
	}

	req := &object.DocSearchRequest{Query: question, Limit: 4}
	hits, err := object.SearchDocuments(owner, store, req, lang)
	if err != nil {
		logs.Warning("chat retrieval: search %s/%s failed: %s", owner, store, err.Error())
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
	return ""
}
