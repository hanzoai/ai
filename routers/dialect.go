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

package routers

import (
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/controllers"
)

// Dialect answers the two decision paths in their own words, whichever layer
// answers — one that wrote its refusal, and one that returned it. A rate limit, a quota, a balance gate and an authorization filter each
// write a refusal of their own before the handler runs; on /v1/decisions every one
// of them leaves as {"error":{"code","message"}}, on /v1/systemone as FastAPI's
// {"detail": ...}, a 402, 429 or 529 carries Retry-After and Retry-After-Ms, and
// every answer carries X-Request-Id — the caller's own, or a fresh one the handler
// also forwards to the service. controllers.Restate is the rule; the handler and
// the ZAP twins apply the same one.
func Dialect(c *zip.Ctx) error {
	path := c.Path()
	if !controllers.DecisionPath(path) {
		return c.Continue()
	}
	rid := controllers.RequestID(c.Header("X-Request-Id"))
	c.Fiber().Request().Header.Set("X-Request-Id", rid)
	if err := c.Continue(); err != nil {
		// A layer that returned its refusal instead of writing it is answered here,
		// in the path's words, rather than by the framework's own renderer.
		status, body, header := controllers.Refusing(path, err, rid)
		for k, v := range header {
			c.SetHeader(k, v)
		}
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(status, body)
	}
	resp := c.Fiber().Response()
	header := map[string]string{}
	for _, k := range []string{"X-Request-Id", "Retry-After", "Retry-After-Ms"} {
		if v := resp.Header.Peek(k); len(v) > 0 {
			header[k] = string(v)
		}
	}
	if body, changed := controllers.Restate(path, resp.StatusCode(), resp.Body(), header, rid); changed {
		resp.SetBody(body)
		resp.Header.SetContentType("application/json")
	}
	for k, v := range header {
		resp.Header.Set(k, v)
	}
	return nil
}
