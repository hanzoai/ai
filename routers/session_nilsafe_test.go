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
	"net/http/httptest"
	"testing"

	beegoctx "github.com/hanzoai/beego/context"
)

// GetSessionUser is the session-read funnel the BeforeRouter filters (RateLimit,
// BalanceGate, Authz) reach via resolveBillingKey — BEFORE the controller. If the
// embedded cloud binary serves a request without beego's SessionStart having run,
// CruSession is nil; the pre-fix code dereferenced it and beego rendered the
// nil-deref as a 500 on EVERY /v1 request. These lock the fail-secure contract: a
// nil (or foreign-typed) session store resolves to "no session user", never a
// panic — so the filters fall through to raw-key limiting and the controller does
// the real Bearer-credential auth.

func TestGetSessionUserNilStoreNoPanic(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-some-probe-token")
	ctx := beegoctx.NewContext()
	ctx.Reset(rec, req)
	// Deliberately DO NOT set ctx.Input.CruSession — this is the nil store the
	// embedded binary can present.

	if u := GetSessionUser(ctx); u != nil {
		t.Fatalf("GetSessionUser(nil store) = %+v, want nil (no session ⇒ no user)", u)
	}
}

func TestGetSessionUserForeignTypeNoPanic(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx := beegoctx.NewContext()
	ctx.Reset(rec, req)
	// A session value that is NOT iam.Claims must not panic the type assertion.
	sess := &fakeSession{data: map[interface{}]interface{}{"user": "not-a-claims-struct"}}
	ctx.Input.CruSession = sess

	if u := GetSessionUser(ctx); u != nil {
		t.Fatalf("GetSessionUser(foreign type) = %+v, want nil (fail-secure)", u)
	}
}
