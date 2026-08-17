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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
)

// signingIn builds a controller holding a live session, the way a request that is
// about to sign in holds one.
func signingIn(t *testing.T, m *web.MemorySessions) (*ApiController, *httptest.ResponseRecorder, web.Store) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/ai/signin", nil)
	ctx := web.NewContext()
	ctx.Reset(rec, r)

	s, err := m.SessionStart(ctx.ResponseWriter, r)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	ctx.Input.CruSession = s

	c := &ApiController{}
	c.Init(ctx, "ApiController", "Signin", nil)
	return c, rec, s
}

// ADOPTING AN IDENTITY RENAMES THE SESSION, AND THE OLD NAME DIES.
//
// This is the composition the sign-in path runs: rotate, then write the claims. It
// is one call precisely so the ORDER cannot be lost — claims written before the
// rotation would sit on the id the caller arrived with, which is the id an attacker
// gets to choose.
func TestAdoptingAnIdentityRotatesTheSession(t *testing.T) {
	m := web.NewMemorySessions("cloud_session_id", time.Hour)
	prev := web.Sessions
	web.Sessions = m
	t.Cleanup(func() { web.Sessions = prev })

	c, _, arrived := signingIn(t, m)
	before := arrived.SessionID()

	c.SetSessionClaims(&iam.Claims{User: iam.User{Owner: "hanzo", Name: "alice"}})

	after := c.Ctx.Input.CruSession.SessionID()
	if after == before {
		t.Fatal("sign-in kept the arriving session id; a planted id would be the authenticated one")
	}

	// The claims must live on the NEW session.
	if u := c.GetSessionUser(); u == nil || u.Name != "alice" {
		t.Fatalf("claims after adoption = %+v, want alice on the new session", u)
	}

	// And the id the caller arrived with must now open nothing.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "cloud_session_id", Value: before})
	revived, err := m.SessionStart(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if revived.SessionID() == before {
		t.Fatal("the arriving id still opens a session after sign-in; it was never retired")
	}
	if revived.Get("user") != nil {
		t.Fatal("the arriving id still carries the signed-in claims; rotation did not move them")
	}
}

// Rotation must not be the thing that breaks a deployment with no session manager
// — the API flow authenticates per request and holds no session at all.
func TestAdoptingAnIdentityWithoutASessionManager(t *testing.T) {
	prev := web.Sessions
	web.Sessions = nil
	t.Cleanup(func() { web.Sessions = prev })

	r := httptest.NewRequest(http.MethodPost, "/v1/ai/signin", nil)
	c := visit("GET", "/v1/")

	// Must not panic, and must still be able to record the identity where a session
	// exists to hold it.
	c.SetSessionClaims(&iam.Claims{User: iam.User{Owner: "hanzo", Name: "alice"}})
}
