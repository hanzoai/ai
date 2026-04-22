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
	"strings"

	"github.com/beego/beego/context"
)

// V1CloudRewriteFilter rewrites /v1/cloud/* requests to /api/* so the
// canonical /<version>/<service>/<path> pattern works without
// duplicating every route in router.go.
//
// Example: /v1/cloud/get-stores → /api/get-stores
func V1CloudRewriteFilter(ctx *context.Context) {
	path := ctx.Request.URL.Path
	if strings.HasPrefix(path, "/v1/cloud/") {
		newPath := "/v1/" + strings.TrimPrefix(path, "/v1/cloud/")
		ctx.Request.URL.Path = newPath
		ctx.Request.RequestURI = newPath
		if ctx.Request.URL.RawQuery != "" {
			ctx.Request.RequestURI = newPath + "?" + ctx.Request.URL.RawQuery
		}
	}
}
