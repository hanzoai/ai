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

package controllers

import (
	"encoding/json"
	"net/http"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// The model-improvement consent endpoints (get-/update-training-contribution) are
// self-scoped to the caller's OWN org and Bearer-reachable via RequirePrincipal, so
// the account-settings opt-in works identically from the console (cookie) and Hanzo
// World (IAM Bearer, no cookie). These tests pin the fail-closed auth invariants and
// the privacy-safe default (OFF) without a DB — object.GetOrgSettings is nil-adapter
// safe, so an org that never set the flag reads unset → enabled:false.

// TestTrainingContribution_NoPrincipalIs401 pins the fail-closed gate: with neither a
// session cookie nor a Bearer, BOTH the read and the write refuse (401) before any
// org resolution or storage access — the consent surface is never anonymously reachable.
func TestTrainingContribution_NoPrincipalIs401(t *testing.T) {
	get, rec := newRewardController("GET", "/v1/get-training-contribution", "", nil)
	get.GetTrainingContribution()
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET no-principal = %d, want 401", rec.Code)
	}

	put, rec2 := newRewardController("POST", "/v1/update-training-contribution", `{"enabled":true}`, nil)
	put.UpdateTrainingContribution()
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("POST no-principal = %d, want 401", rec2.Code)
	}
}

// TestTrainingContribution_RejectsAnonymousGuest ensures a synthesized guest (bound
// to the shared deployment org by anonymousSignin) can never read or flip that org's
// opt-in: opting IN is a deliberate act by a REAL signed-in identity for its OWN org.
func TestTrainingContribution_RejectsAnonymousGuest(t *testing.T) {
	guest := &iam.User{Owner: "hanzo", Type: "anonymous-user", Name: "u-12345678"}

	get, rec := newRewardController("GET", "/v1/get-training-contribution", "", guest)
	get.GetTrainingContribution()
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET anonymous = %d, want 401", rec.Code)
	}

	put, rec2 := newRewardController("POST", "/v1/update-training-contribution", `{"enabled":true}`, guest)
	put.UpdateTrainingContribution()
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("POST anonymous = %d, want 401", rec2.Code)
	}
}

// TestGetTrainingContribution_DefaultOff proves the privacy-safe default: a real
// signed-in principal whose org never set the flag reads enabled:false (opted OUT).
func TestGetTrainingContribution_DefaultOff(t *testing.T) {
	// A unique owner keeps the 60s org-settings cache from leaking another test's row.
	user := &iam.User{Owner: "consent-default-off-org", Name: "dave"}
	c, rec := newRewardController("GET", "/v1/get-training-contribution", "", user)
	c.GetTrainingContribution()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET signed-in = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var env struct {
		Status string `json:"status"`
		Data   struct {
			Enabled bool `json:"enabled"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, rec.Body.String())
	}
	if env.Data.Enabled {
		t.Errorf("default contribution = true, want false (privacy-safe default OFF)")
	}
}
