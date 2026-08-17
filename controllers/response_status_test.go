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
)

// TestResponseForbiddenAndUnauthorizedStatus is the R5 mechanism assertion: the
// controller auth-deny helpers emit REAL 403/401 statuses (not The router's default
// 200). The controller deny sites (chat/message/scale) were converted from
// c.ResponseError (HTTP 200) to these helpers.
func TestResponseForbiddenAndUnauthorizedStatus(t *testing.T) {
	c := visit(http.MethodGet, "/v1/x")
	c.ResponseForbidden("auth:Unauthorized operation")
	if answered(c) != 403 {
		t.Errorf("ResponseForbidden status = %d, want 403", answered(c))
	}

	c2 := visit(http.MethodGet, "/v1/x")
	c2.ResponseUnauthorized("auth:Please sign in first")
	if answered(c2) != 401 {
		t.Errorf("ResponseUnauthorized status = %d, want 401", answered(c2))
	}

	// Contrast: plain ResponseError still emits 200 (why the deny sites had to move).
	c3 := visit(http.MethodGet, "/v1/x")
	c3.ResponseError("some non-auth error")
	if answered(c3) != 200 {
		t.Errorf("plain ResponseError status = %d, want 200 (baseline)", answered(c3))
	}
}
