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
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/object"
)

// TrafficTapFilter is a BeforeRouter tap that folds each genuine inbound /v1 API
// request into the in-process traffic aggregate (object.GlobalTraffic), keyed by the
// EDGE-supplied geo (Cloudflare CF-IPCountry + optional CF-Region-Code) and a coarse
// service class derived from the path. It powers the public world.hanzo.ai live-
// traffic globe.
//
// PRIVACY (load-bearing): it reads ONLY the country/region geo headers. It never
// reads CF-Connecting-IP or any client IP, never hashes anything, and hands the
// aggregate nothing but (country, region, service) — Record has no IP parameter to
// pass one to. It is a pure side effect: it never writes a response, never blocks,
// never errors, and returns immediately, so its position in the filter chain cannot
// affect any request's outcome.
func TrafficTapFilter(c *zip.Ctx) error {
	path := c.Path()
	if !object.TrafficShouldRecord(path, c.Method()) {
		return c.Continue()
	}
	object.GlobalTraffic.Record(
		c.Header("CF-IPCountry"),
		c.Header("CF-Region-Code"),
		object.TrafficServiceClass(path),
	)
	return c.Continue()
}
