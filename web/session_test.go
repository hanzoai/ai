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

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestMemorySessionsStartAndReuse proves a cookieless request creates a session
// and sets the cookie, and a follow-up carrying that cookie reuses the same
// store (values persist).
func TestMemorySessionsStartAndReuse(t *testing.T) {
	m := NewMemorySessions("cloud_session_id", time.Hour)

	rec := httptest.NewRecorder()
	s1, err := m.SessionStart(rec, httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	s1.Set("user", "alice")

	setCookie := rec.Result().Cookies()
	if len(setCookie) == 0 || setCookie[0].Name != "cloud_session_id" || setCookie[0].Value != s1.SessionID() {
		t.Fatalf("SessionStart must set the session cookie to the session id; got %+v", setCookie)
	}
	if !setCookie[0].HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(&http.Cookie{Name: "cloud_session_id", Value: s1.SessionID()})
	s2, err := m.SessionStart(httptest.NewRecorder(), req2)
	if err != nil {
		t.Fatalf("SessionStart reuse: %v", err)
	}
	if s2.SessionID() != s1.SessionID() {
		t.Fatalf("cookie must reuse the same session: %s != %s", s2.SessionID(), s1.SessionID())
	}
	if s2.Get("user") != "alice" {
		t.Fatalf("session value must persist across requests, got %v", s2.Get("user"))
	}
}

// TestMemorySessionsUniquePerCookielessRequest proves two cookieless requests
// get distinct sessions.
func TestMemorySessionsUniquePerCookielessRequest(t *testing.T) {
	m := NewMemorySessions("cloud_session_id", time.Hour)
	a, _ := m.SessionStart(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	b, _ := m.SessionStart(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if a.SessionID() == b.SessionID() {
		t.Fatal("distinct cookieless requests must get distinct sessions")
	}
}

// TestMemorySessionsDestroy proves SessionDestroy drops the session so a later
// request with that cookie gets a fresh, empty one.
func TestMemorySessionsDestroy(t *testing.T) {
	m := NewMemorySessions("cloud_session_id", time.Hour)
	s, _ := m.SessionStart(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	s.Set("user", "bob")
	id := s.SessionID()

	if err := m.SessionDestroy(id); err != nil {
		t.Fatalf("SessionDestroy: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "cloud_session_id", Value: id})
	s2, _ := m.SessionStart(httptest.NewRecorder(), req)
	if s2.SessionID() == id {
		t.Fatal("destroyed session id must not be reused")
	}
	if s2.Get("user") != nil {
		t.Fatal("destroyed session must not carry old values")
	}
}

// TestMemorySessionsSatisfiesRouter proves the manager binds to the router and
// a controller reads the session through it.
func TestMemorySessionsSatisfiesRouter(t *testing.T) {
	m := NewMemorySessions("cloud_session_id", time.Hour)
	var _ SessionManager = m // interface satisfaction

	r := NewRouter()
	r.UseSessions(m)
	r.Router("/v1/whoami", &sessionProbe{}, "GET:Who")
	// First request establishes a session and sets it — the probe reads empty.
	if code := serve(r, "GET", "/v1/whoami").Code; code != http.StatusOK {
		t.Fatalf("wired session route code = %d, want 200", code)
	}
}
