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
	"net/http"
	"strings"

	"github.com/hanzoai/ai/controllers"
	"github.com/zap-proto/zip"
)

// AliasFilter rewrites a request that names an alias (models.yaml alias_of) to name
// the id the alias stands for, before anything reads the model: routing, the gate,
// the plan limits, the handler and the ledger all see that id, so an alias is
// served and billed as it. A decision path is left alone: its handler resolves an
// alias after the credential. So is a body with a Content-Encoding: this runs ahead
// of every credential, and reading a coded body's model means decoding it for a
// sender nobody has authenticated. Its model reaches the handler as sent.
func AliasFilter(c *zip.Ctx) error {
	if c.Method() != http.MethodPost || controllers.DecisionPath(c.Path()) || coded(c) {
		return c.Continue()
	}
	if id, ok := canonical(requestedModel(c)); ok {
		if body, ok := controllers.WithModel(c.Body(), id); ok {
			req := c.Fiber().Request()
			req.SetBody(body)
			req.Header.Del("Content-Encoding")
		}
	}
	return c.Continue()
}

// coded reports a request body sent with a Content-Encoding other than identity.
func coded(c *zip.Ctx) bool {
	e := strings.TrimSpace(string(c.Fiber().Request().Header.ContentEncoding()))
	return e != "" && !strings.EqualFold(e, "identity")
}

// canonical is controllers.Canonical, indirected so the filter's tests state the
// alias table directly.
var canonical = controllers.Canonical
