// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"net/http"
	"os"
	"strings"

	"github.com/beego/beego/logs"
	iamsdk "github.com/hanzoid/go-sdk/casdoorsdk"
	"github.com/hanzoai/cloud/model"
	"github.com/hanzoai/cloud/object"
)

// retrievalOwner returns the IAM org whose search index should be queried.
// Resolution order: authenticated user owner, widget key origin mapping, empty.
func retrievalOwner(authUser *iamsdk.User, token, origin, referer string) string {
	if authUser != nil && authUser.Owner != "" {
		return authUser.Owner
	}
	if isWidgetKey(token) {
		h := origin
		if h == "" {
			h = referer
		}
		return resolveOwnerFromOrigin(h)
	}
	return ""
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
