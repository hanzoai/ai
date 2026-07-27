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

package routers

import (
	"sync"

	"github.com/hanzoai/ai/web"
)

var installFiltersOnce sync.Once

// InstallFilters inserts the request filter chain on App in order: twelve
// BeforeRouter filters (CORS, the traffic tap, security headers, rate limit,
// auto-signin, the balance gate, static, tenant, authz, prometheus and message
// recording) and two AfterExec filters (message recording and the secure-cookie
// writer). It is idempotent, so the runtime and the route tests share one wiring.
//
// There are no path-rewriting filters here any more. Two used to lead the chain:
// /v1/cloud/* → /v1/* rewrote EVERY route into a second address, and /v1/iam/*
// did the same for the four account endpoints. Between them a single endpoint
// answered at three URLs, each one a place a policy could be applied
// inconsistently. Resources now live at exactly one address, generated from the
// table in resources.go — so there is nothing left to rewrite.
func InstallFilters() {
	installFiltersOnce.Do(func() {
		App.InsertFilter("*", web.BeforeRouter, CorsFilter)
		// Live request-geo tap: folds each inbound /v1 API hit into the
		// in-process traffic aggregate (edge country/region + service class
		// only — never an IP). A pure side effect that never blocks or writes,
		// so it rides high in the chain to count LB hits honestly. Powers the
		// public /v1/traffic/globe surface.
		App.InsertFilter("/v1/*", web.BeforeRouter, TrafficTapFilter)
		App.InsertFilter("*", web.BeforeRouter, HstsFilter)
		App.InsertFilter("*", web.BeforeRouter, CacheControlFilter)
		App.InsertFilter("*", web.BeforeRouter, RateLimitFilter)
		App.InsertFilter("*", web.BeforeRouter, AutoSigninFilter)
		App.InsertFilter("*", web.BeforeRouter, BalanceGateFilter)
		App.InsertFilter("*", web.BeforeRouter, StaticFilter)
		App.InsertFilter("*", web.BeforeRouter, TenantContextFilter)
		App.InsertFilter("*", web.BeforeRouter, AuthzFilter)
		App.InsertFilter("*", web.BeforeRouter, PrometheusFilter)
		App.InsertFilter("*", web.BeforeRouter, RecordMessage)
		App.InsertFilter("*", web.AfterExec, AfterRecordMessage, false)
		App.InsertFilter("*", web.AfterExec, SecureCookieFilter, false)
	})
}
