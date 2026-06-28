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
	"net/http"
	"net/http/httptest"
	"testing"

	beegoctx "github.com/beego/beego/context"
	"github.com/hanzoai/ai/controllers"
	iam "github.com/hanzoai/iam"
)

// fakeSession is a minimal in-memory session.Store for filter tests (the real
// session manager is not wired in unit tests, so CruSession would be nil).
type fakeSession struct{ data map[interface{}]interface{} }

func (s *fakeSession) Set(k, v interface{}) error    { s.data[k] = v; return nil }
func (s *fakeSession) Get(k interface{}) interface{} { return s.data[k] }
func (s *fakeSession) Delete(k interface{}) error    { delete(s.data, k); return nil }
func (s *fakeSession) SessionID() string             { return "test" }
func (s *fakeSession) SessionRelease(http.ResponseWriter) {}
func (s *fakeSession) Flush() error {
	s.data = map[interface{}]interface{}{}
	return nil
}

// newFilterCtx builds a beego context with a session optionally carrying user as
// the authenticated principal.
func newFilterCtx(method, path string, user *iam.User) (*beegoctx.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	ctx := beegoctx.NewContext()
	ctx.Reset(rec, req)
	sess := &fakeSession{data: map[interface{}]interface{}{}}
	if user != nil {
		sess.data["user"] = iam.Claims{User: *user}
	}
	ctx.Input.CruSession = sess
	return ctx, rec
}

// TestRequiresGlobalAdminClassification locks in which endpoints are platform
// (global-admin) sensitive and which stay public — crucially get-models stays out.
func TestRequiresGlobalAdminClassification(t *testing.T) {
	sensitive := []string{
		"get-providers", "get-provider", "get-global-providers",
		"add-provider", "update-provider", "delete-provider",
		"get-storage-providers", "get-model-routes", "reload-model-config",
		"get-nodes", "get-pods", "get-k8s-status",
	}
	for _, e := range sensitive {
		if !requiresGlobalAdmin(e) {
			t.Errorf("%q must require global admin", e)
		}
	}
	public := []string{"models", "get-models", "get-account", "get-chats", "get-messages", "chat/completions"}
	for _, e := range public {
		if requiresGlobalAdmin(e) {
			t.Errorf("%q must NOT require global admin (public/benign)", e)
		}
	}
}

// TestSensitiveUnauthorized401 — an unauthenticated get-provider is 401 (not the
// old preview-mode default-open 200 disclosure).
func TestSensitiveUnauthorized401(t *testing.T) {
	for _, path := range []string{"/v1/get-provider", "/v1/get-providers"} {
		ctx, rec := newFilterCtx("GET", path, nil)
		permissionFilter(ctx)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("unauth %s = %d, want 401", path, rec.Code)
		}
	}
}

// TestSensitiveOrgAdminForbidden403 — an ORG admin (not global) is 403 on
// provider config.
func TestSensitiveOrgAdminForbidden403(t *testing.T) {
	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}
	ctx, rec := newFilterCtx("GET", "/v1/get-providers", orgAdmin)
	permissionFilter(ctx)
	if rec.Code != http.StatusForbidden {
		t.Errorf("org-admin get-providers = %d, want 403", rec.Code)
	}
}

// TestSensitiveGlobalAdminAllowed — a global admin passes the gate untouched.
func TestSensitiveGlobalAdminAllowed(t *testing.T) {
	globalAdmin := &iam.User{Owner: "admin", Name: "admin", IsAdmin: true}
	ctx, rec := newFilterCtx("GET", "/v1/get-providers", globalAdmin)
	permissionFilter(ctx)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("global-admin get-providers wrote a denial (code=%d, body=%q); want pass-through", rec.Code, rec.Body.String())
	}
}

// TestDenyRequestForbidden403 — explicit denials are 403, not 200 (#9).
func TestDenyRequestForbidden403(t *testing.T) {
	ctx, rec := newFilterCtx("POST", "/v1/some-op", nil)
	controllers.DenyRequest(ctx)
	if rec.Code != http.StatusForbidden {
		t.Errorf("DenyRequest = %d, want 403", rec.Code)
	}
}
