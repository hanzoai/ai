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

package controllers

import (
	"net/http"
	"testing"

	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"
)

// TestNakedHandlersRequireSuperAdmin asserts that every platform-sensitive handler
// that previously relied SOLELY on the routers.permissionFilter gate now ALSO
// self-guards with c.RequireSuperAdmin() as its first statement (defense in depth,
// matching the provider_admin handlers). Even if the filter is ever bypassed (the
// path-normalization class this PR also closes), provider / model-route mutation and
// cluster (node/k8s) introspection can no longer be reached unauthenticated
// (=> 401) or by a mere org admin (=> 403).
//
// Only the DENY paths are exercised: RequireSuperAdmin() is the FIRST statement in
// each handler and returns before any request-body parse or datastore/object call, so
// no live DB is needed. The super-admin PASS path is covered by
// TestRequireSuperAdmin_SuperAdminPasses plus each handler's own downstream tests.
func TestNakedHandlersRequireSuperAdmin(t *testing.T) {
	// Every handler this PR added the c.RequireSuperAdmin() guard to.
	handlers := []struct {
		name string
		call func(*ApiController)
	}{
		{"UpdateProvider", (*ApiController).UpdateProvider},
		{"DeleteProvider", (*ApiController).DeleteProvider},
		{"RefreshMcpTools", (*ApiController).RefreshMcpTools},
		{"GetModelRoutes", (*ApiController).GetModelRoutes},
		{"GetModelRoute", (*ApiController).GetModelRoute},
		{"AddModelRoute", (*ApiController).AddModelRoute},
		{"UpdateModelRoute", (*ApiController).UpdateModelRoute},
		{"DeleteModelRoute", (*ApiController).DeleteModelRoute},
		{"ReloadModelConfig", (*ApiController).ReloadModelConfig},
		{"GetNodes", (*ApiController).GetNodes},
		{"GetK8sStatus", (*ApiController).GetK8sStatus},
	}

	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}

	for _, h := range handlers {
		// Anonymous → 401 (fail closed, before any body/DB read).
		c := toggling("")
		h.call(c)
		if answered(c) != http.StatusUnauthorized {
			t.Errorf("%s anonymous = %d, want 401 (naked handler must self-auth)", h.name, answered(c))
		}

		// Org admin (not global) → 403 (no org-admin → platform-admin escalation).
		c2 := toggling(authtest.Bearer(t, *orgAdmin))
		h.call(c2)
		if answered(c2) != http.StatusForbidden {
			t.Errorf("%s org-admin = %d, want 403 (not a platform admin)", h.name, answered(c2))
		}
	}
}
