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

package routers

import (
	"sync"

	"github.com/zap-proto/zip"
)

var installFiltersOnce sync.Once

// InstallFilters puts the request chain in front of every route, in this order.
// Idempotent, so the runtime and the route tests share one wiring.
//
// EACH ONE DECIDES WHETHER TO CONTINUE, and says so by returning c.Continue() or
// not. That used to be implied: a filter wrote a response and returned, and the
// router inferred a refusal from the fact that something had been written. The
// refusals are now the shape of the code.
//
// There is no "after" list. Recording used to be two filters — one before the
// router, one after the handler — passing a username between them through a
// request parameter; it is one middleware holding a local across the handler.
// Cookie rewriting was the other after-filter, and it went with the session store
// it patched.
//
// Two filters are gone entirely, and both were doing somebody else's job:
//
//   - Auto-signin accepted an MD5 of clientId:clientSecret, or HTTP Basic against
//     the configured pair, and minted an IsAdmin identity into a per-pod in-memory
//     store. IAM mints credentials; this service reads the one presented.
//   - Static served a landing folder, the admin bundle and /storage from a path
//     taken off the URL, and rewrote main.<hash>.js on the way out. Static content
//     is ingress and the static plugin. Every route here is /v1.
//
// There are no path-rewriting filters either. Two used to lead the chain:
// /v1/cloud/* → /v1/* rewrote EVERY route into a second address, and /v1/iam/*
// did the same for four account endpoints. Between them a single endpoint answered
// at three URLs, each one a place a policy could be applied inconsistently.
// Resources live at exactly one address, generated from the table in resources.go.
func InstallFilters(app *zip.App) {
	installFiltersOnce.Do(func() {
		app.Use(zip.H(CorsFilter))
		// Live request-geo tap: folds each inbound hit into the in-process traffic
		// aggregate (edge country/region + service class only — never an IP). A pure
		// side effect that never blocks or writes, so it rides high in the chain to
		// count load-balancer hits honestly. Powers /v1/traffic/globe.
		app.Use(zip.H(TrafficTapFilter))
		app.Use(zip.H(HstsFilter))
		app.Use(zip.H(CacheControlFilter))
		app.Use(zip.H(RateLimitFilter))
		// Ahead of every filter that reads a token. Below this line a token that does
		// not parse means the TOKEN is bad; above it, it could also mean no signing
		// cert was ever established — and the two are indistinguishable at the parse.
		// Refusing here with 503 keeps that ambiguity out of the chain.
		app.Use(zip.H(AuthAvailableFilter))
		app.Use(zip.H(BalanceGateFilter))
		app.Use(zip.H(TenantContextFilter))
		app.Use(zip.H(AuthzFilter))
		app.Use(zip.H(PrometheusFilter))
		app.Use(zip.H(Record))
	})
}
