// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"strings"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/retrieval"
)

// retrievalOwner returns the IAM org whose search index should be queried: the
// org of the principal the request already resolved to. It is NEVER derived from
// the request Origin/Referer, which a client can forge to read another tenant's
// RAG store (cross-tenant disclosure). No principal means no tenant, and the
// caller reads nothing.
func retrievalOwner(authUser *iam.User) string {
	if authUser != nil && authUser.Owner != "" {
		return authUser.Owner
	}
	return ""
}

// header reads one request header, the shape package retrieval asks for.
func (c *ApiController) header(name string) string { return c.Header(name) }

// retrievalStore names the store to search (retrieval.Store).
func (c *ApiController) retrievalStore() string {
	return retrieval.Store(c.header, c.Body())
}

// retrievalEnabled decides whether to augment the prompt with retrieved docs
// (retrieval.Asked — the one statement of it, which the host in front reads too).
func (c *ApiController) retrievalEnabled() bool {
	return retrieval.Asked(c.header, c.Body())
}

// retrieveKnowledgeIfEnabled pulls top-K relevant documents from the owner's
// search store. Returns an empty slice on any failure so the LLM call still
// proceeds without RAG.
func (c *ApiController) retrieveKnowledgeIfEnabled(
	question, owner, store, lang string,
) []*model.RawMessage {
	empty := []*model.RawMessage{}
	if !c.retrievalEnabled() {
		return empty
	}
	if owner == "" {
		return empty
	}
	if store == "" {
		store = c.Input().Get("store")
	}
	// Brand-neutral per-org default (white-label): the owner prefix on the index
	// (`{owner}-{store}-docs`) is what isolates each org, so the store slug itself
	// must NOT bake in a brand. Every org's assistant reads its OWN docs store —
	// and object.ResolveStore is what stops a caller naming its way out of that
	// prefix. RAG is best-effort here, so a refused store reads no documents rather
	// than failing the completion.
	store, err := object.ResolveStore(owner, store, object.DefaultDocsStore)
	if err != nil {
		return empty
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

func bearerToken(authorization, iamCookie string) string {
	if after, ok := strings.CutPrefix(authorization, "Bearer "); ok {
		return after
	}
	// First-party cookie fallback: a browser cookie session carries no
	// Authorization header, but Signin persisted the verified IAM access token as
	// the hanzo_iam_token cookie so the stateless validator can re-derive identity
	// when the in-memory the router session is gone (self-heal). See iamTokenCookieName.
	if iamCookie != "" {
		return iamCookie
	}
	return ""
}
