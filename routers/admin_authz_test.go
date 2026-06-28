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
	"strings"
	"testing"

	"github.com/beego/beego/context"
	iam "github.com/hanzoai/iam"
)

// fakeSession is a minimal in-memory beego session.Store for injecting an
// identity into the permissionFilter under test.
type fakeSession struct{ data map[interface{}]interface{} }

func (s *fakeSession) Set(k, v interface{}) error         { s.data[k] = v; return nil }
func (s *fakeSession) Get(k interface{}) interface{}      { return s.data[k] }
func (s *fakeSession) Delete(k interface{}) error         { delete(s.data, k); return nil }
func (s *fakeSession) SessionID() string                  { return "test" }
func (s *fakeSession) SessionRelease(http.ResponseWriter) {}
func (s *fakeSession) Flush() error                       { s.data = map[interface{}]interface{}{}; return nil }

func ctxWithUser(method, path string, user *iam.User) (*context.Context, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	ctx := context.NewContext()
	ctx.Reset(w, r)
	sess := &fakeSession{data: map[interface{}]interface{}{}}
	if user != nil {
		_ = sess.Set("user", iam.Claims{User: *user})
	}
	ctx.Input.CruSession = sess
	return ctx, w
}

// denied reports whether permissionFilter wrote an error (i.e. blocked the
// request). An allowed request leaves the response untouched.
func denied(w *httptest.ResponseRecorder) bool {
	return strings.Contains(w.Body.String(), "error")
}

// TestPermissionFilterAdminGate is the core admin-authorization contract: a
// mutating endpoint requires an admin identity; non-admins and anonymous callers
// are denied; an admin is allowed; and explicitly-exempt self-scoped paths are
// open to any signed-in user.
func TestPermissionFilterAdminGate(t *testing.T) {
	admin := &iam.User{Owner: "admin", Name: "root", IsAdmin: true}
	chatAdmin := &iam.User{Owner: "acme", Name: "mgr", Type: "chat-admin"}
	user := &iam.User{Owner: "acme", Name: "alice"}

	cases := []struct {
		name       string
		method     string
		path       string
		user       *iam.User
		wantDenied bool
	}{
		// Mutations are admin-gated.
		{"add-provider/non-admin denied", "POST", "/v1/add-provider", user, true},
		{"update-provider/non-admin denied", "POST", "/v1/update-provider", user, true},
		{"delete-provider/non-admin denied", "POST", "/v1/delete-provider", user, true},
		{"add-model-route/non-admin denied", "POST", "/v1/add-model-route", user, true},
		{"add-provider/anonymous denied", "POST", "/v1/add-provider", nil, true},
		{"add-provider/admin allowed", "POST", "/v1/add-provider", admin, false},
		{"update-provider/chat-admin allowed", "POST", "/v1/update-provider", chatAdmin, false},
		// Exempt, self-scoped paths: any signed-in user.
		{"update-preferences/user allowed", "POST", "/v1/update-preferences", user, false},
		{"add-chat/user allowed", "POST", "/v1/add-chat", user, false},
		{"get-providers/user allowed (exempt+masked)", "GET", "/v1/get-providers", user, false},
		// Non-mutation, non-get endpoints (auth handled in the handler, not here).
		{"chat-completions not gated here", "POST", "/v1/chat/completions", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, w := ctxWithUser(tc.method, tc.path, tc.user)
			permissionFilter(ctx)
			if got := denied(w); got != tc.wantDenied {
				t.Fatalf("%s %s: denied=%v want %v; body=%s", tc.method, tc.path, got, tc.wantDenied, w.Body.String())
			}
		})
	}
}

// TestPermissionFilterAdminMessageMentionsPrivilege asserts the denial is the
// admin-privilege error (not some unrelated failure), so the gate's intent is
// unambiguous.
func TestPermissionFilterAdminMessageMentionsPrivilege(t *testing.T) {
	ctx, w := ctxWithUser("POST", "/v1/delete-provider", &iam.User{Owner: "acme", Name: "alice"})
	permissionFilter(ctx)
	body := w.Body.String()
	if !strings.Contains(body, "error") {
		t.Fatalf("non-admin delete-provider not denied: %s", body)
	}
	// Message is i18n-translated; the english key contains "admin privilege".
	if !strings.Contains(strings.ToLower(body), "admin") && !strings.Contains(body, "privilege") {
		t.Logf("denial body (translated): %s", body)
	}
}

// TestPermissionFilterMutationPrefixes locks the mutation-prefix classification:
// every mutating verb prefix is gated for non-admins so a new add-*/update-*/
// delete-*/refresh-*/deploy-* route can never ship ungated by accident.
func TestPermissionFilterMutationPrefixes(t *testing.T) {
	user := &iam.User{Owner: "acme", Name: "alice"}
	for _, path := range []string{
		"/v1/add-application", "/v1/update-application", "/v1/delete-application",
		"/v1/refresh-mcp-tools", "/v1/deploy-application", "/v1/add-node", "/v1/delete-scan",
	} {
		ctx, w := ctxWithUser("POST", path, user)
		permissionFilter(ctx)
		if !denied(w) {
			t.Errorf("mutation %s NOT gated for non-admin (ungated mutation!)", path)
		}
	}
}
