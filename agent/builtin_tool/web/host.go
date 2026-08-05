// Copyright (c) 2014-present Hanzo AI, Inc.
// Licensed under Apache-2.0. See LICENSE.

// Package webtools gives every Responses-API agent the three capabilities the
// platform already serves and no agent could reach: web search, page fetch, and
// deep research.
//
// The registry held five time tools, so an agent asking to search the web had
// nothing to call — while /v1/websearch/search, /v1/crawl and /v1/ask were live the
// whole time. These are those endpoints as tools.
//
// WHY EVERY BACKEND IS A SEAM. This package is a LEAF and must stay one:
// `object` imports `agent`, `agent` imports this registry, so a tool that reached
// into object for crawl closed the loop — agent → builtin_tool → web → object →
// agent — and the module stopped compiling. Search and research are served by the
// HOST (hanzoai/cloud), which imports this module, so they could never have been
// direct calls either. All three are installed from outside: crawl by this module's
// own bootstrap, search and research by the host at mount time, beside the balance
// and tier readers it already installs.
//
// It is also the shape this codebase learned the hard way: the ai module once
// reached its own platform over api.hanzo.ai, which carries a customer credential
// and answered 401 to a service, and every completion fail-closed at 503 on a
// perfectly healthy pod. The edge is for customers; in-process is for us.
//
// UNSET IS REFUSED, NEVER FAKED. A capability the host did not install answers that
// it is unavailable in this deployment. An agent told "no results" would conclude the
// web holds nothing on the subject and answer from memory — which is a fabricated
// answer with a confident tone. Absent and empty are different facts.
package webtools

import (
	"context"
	"errors"
	"sync"
)

// FetchResult is one fetched page, reduced to what a model can read and cite.
type FetchResult struct {
	URL      string
	Title    string
	Markdown string
	Success  bool
}

// FetchFunc fetches pages as Markdown. Installed by this module's bootstrap.
type FetchFunc func(ctx context.Context, urls []string) ([]FetchResult, error)

// SearchResult is one web hit. Deliberately the smallest shape that is useful to a
// model: enough to cite, not enough to bloat a context window.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// SearchFunc runs a web meta-search. Installed by the host.
type SearchFunc func(ctx context.Context, query string, limit int) ([]SearchResult, error)

// ResearchFunc runs a grounded, multi-source investigation and returns the report
// with its citations inline. Installed by the host.
type ResearchFunc func(ctx context.Context, question string) (string, error)

var (
	mu         sync.RWMutex
	searchFn   SearchFunc
	researchFn ResearchFunc
	fetchFn    FetchFunc
)

// ErrNotConfigured is what a tool answers when the host installed no backend for it.
// It names the deployment, not the query, so a model cannot read it as "nothing found".
var ErrNotConfigured = errors.New("this capability is not available in this deployment")

// SetFetch installs the page fetcher (nil clears it).
func SetFetch(f FetchFunc) { mu.Lock(); fetchFn = f; mu.Unlock() }

// Fetch returns the installed fetcher, or nil.
func Fetch() FetchFunc { mu.RLock(); defer mu.RUnlock(); return fetchFn }

// SetSearch installs the host's web search (nil clears it).
func SetSearch(f SearchFunc) { mu.Lock(); searchFn = f; mu.Unlock() }

// SetResearch installs the host's deep research (nil clears it).
func SetResearch(f ResearchFunc) { mu.Lock(); researchFn = f; mu.Unlock() }

// Search returns the installed search, or nil.
func Search() SearchFunc { mu.RLock(); defer mu.RUnlock(); return searchFn }

// Research returns the installed research, or nil.
func Research() ResearchFunc { mu.RLock(); defer mu.RUnlock(); return researchFn }
