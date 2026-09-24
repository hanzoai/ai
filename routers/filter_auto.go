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
	"net/http"
	"strings"

	"github.com/hanzoai/ai/controllers"
	"github.com/zap-proto/zip"
)

// AutoRouteFilter resolves a completion that names the virtual `auto`/`zen-router`
// model to the SKU that will serve it, before BalanceGateFilter reads the request.
// The gate and the plan limits then price and admit that SKU, never the virtual id.
// See controllers.RouteAuto.
func AutoRouteFilter(c *zip.Ctx) error {
	if c.Method() == http.MethodPost && completes(c.Path()) {
		routeAuto(c)
	}
	return c.Continue()
}

// routeAuto is controllers.RouteAuto on this request, indirected so the gate's tests
// state the routing decision directly.
var routeAuto = func(c *zip.Ctx) { (&controllers.ApiController{Ctx: c}).RouteAuto() }

// completes reports whether a path is one the completion pipeline serves with the
// model its body names.
func completes(path string) bool {
	switch strings.ToLower(strings.TrimRight(path, "/")) {
	case "/v1/chat", "/v1/chat/completions", "/v1/responses":
		return true
	}
	return false
}
