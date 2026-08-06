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
	"net/http/httptest"
	"testing"

	web "github.com/hanzoai/ai/web"
)

// newRecorderController builds an ApiController wired to an httptest recorder so a
// handler's HTTP status can be asserted directly.
func newRecorderController() (*ApiController, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	ctx := web.NewContext()
	ctx.Reset(rec, httptest.NewRequest("GET", "/v1/x", nil))
	c := &ApiController{}
	c.Init(ctx, "ApiController", "X", nil)
	return c, rec
}

// TestResponseForbiddenAndUnauthorizedStatus is the R5 mechanism assertion: the
// controller auth-deny helpers emit REAL 403/401 statuses (not Beego's default
// 200). The 9 controller deny sites (chat/message/patient/scale) were converted
// from c.ResponseError (HTTP 200) to these helpers.
func TestResponseForbiddenAndUnauthorizedStatus(t *testing.T) {
	c, rec := newRecorderController()
	c.ResponseForbidden("auth:Unauthorized operation")
	if rec.Code != 403 {
		t.Errorf("ResponseForbidden status = %d, want 403", rec.Code)
	}

	c2, rec2 := newRecorderController()
	c2.ResponseUnauthorized("auth:Please sign in first")
	if rec2.Code != 401 {
		t.Errorf("ResponseUnauthorized status = %d, want 401", rec2.Code)
	}

	// Contrast: plain ResponseError still emits 200 (why the deny sites had to move).
	c3, rec3 := newRecorderController()
	c3.ResponseError("some non-auth error")
	if rec3.Code != 200 {
		t.Errorf("plain ResponseError status = %d, want 200 (baseline)", rec3.Code)
	}
}
