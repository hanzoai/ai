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

	iam "github.com/hanzoai/iam"
)

// TestNakedHandlersRequireGlobalAdmin asserts that every platform-sensitive handler
// that previously relied SOLELY on the routers.permissionFilter gate now ALSO
// self-guards with c.RequireGlobalAdmin() as its first statement (defense in depth,
// matching the provider_admin handlers). Even if the filter is ever bypassed (the
// path-normalization class this PR also closes), provider / model-route mutation and
// cluster (node/pod/container/image/machine/k8s) introspection can no longer be
// reached unauthenticated (=> 401) or by a mere org admin (=> 403).
//
// Only the DENY paths are exercised: RequireGlobalAdmin() is the FIRST statement in
// each handler and returns before any request-body parse or datastore/object call, so
// no live DB is needed. The global-admin PASS path is covered by
// TestRequireGlobalAdmin_GlobalAdminPasses plus each handler's own downstream tests.
func TestNakedHandlersRequireGlobalAdmin(t *testing.T) {
	t.Setenv("globalAdminOrgs", "") // default {admin, built-in} global-admin set

	// Every handler this PR added the c.RequireGlobalAdmin() guard to.
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
		{"GetPods", (*ApiController).GetPods},
		{"GetContainers", (*ApiController).GetContainers},
		{"GetImages", (*ApiController).GetImages},
		{"GetMachines", (*ApiController).GetMachines},
		{"GetK8sStatus", (*ApiController).GetK8sStatus},
	}

	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}

	for _, h := range handlers {
		// Anonymous → 401 (fail closed, before any body/DB read).
		c, rec := newGuardController(nil)
		h.call(c)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous = %d, want 401 (naked handler must self-auth)", h.name, rec.Code)
		}

		// Org admin (not global) → 403 (no org-admin → platform-admin escalation).
		c2, rec2 := newGuardController(orgAdmin)
		h.call(c2)
		if rec2.Code != http.StatusForbidden {
			t.Errorf("%s org-admin = %d, want 403 (not a platform admin)", h.name, rec2.Code)
		}
	}
}
