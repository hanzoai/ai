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

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// start opens a session the way a request does, and answers the store plus the
// cookie value the browser would now be holding.
func start(t *testing.T, m *MemorySessions, cookie string) (Store, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: m.cookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	s, err := m.SessionStart(rec, r)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	return s, s.SessionID()
}

// setCookie reads the cookie this manager just wrote, or nil when it wrote none.
func setCookie(t *testing.T, m *MemorySessions, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == m.cookieName {
			return c
		}
	}
	return nil
}

// THE POINT OF THE WHOLE EXERCISE: the id a caller held before they proved who
// they are is not the id they hold after, and the old one is DEAD.
//
// Without this, anyone able to plant a cookie value chooses the identifier their
// victim's sign-in will authenticate — a session that never had to be stolen.
func TestRotationRetiresTheOldIdentifier(t *testing.T) {
	m := NewMemorySessions("cloud_session_id", time.Hour)
	old, oldID := start(t, m, "")
	if err := old.Set("user", "planted"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	fresh, err := m.RegenerateID(rec, old)
	if err != nil {
		t.Fatalf("RegenerateID: %v", err)
	}

	if fresh.SessionID() == oldID {
		t.Fatal("the session kept its identifier across rotation; a planted id would survive sign-in")
	}

	// The old id must no longer resolve to that session. Presenting it again has to
	// land on a NEW, empty session rather than the one being retired.
	revived, revivedID := start(t, m, oldID)
	if revivedID == oldID {
		t.Fatal("the retired identifier still opens a session; rotation left a second key to the same door")
	}
	if got := revived.Get("user"); got != nil {
		t.Fatalf("the retired identifier still carries %v; the old session was not destroyed", got)
	}
}

// Rotation renames a session; it does not empty it. Anything already on the
// session has to survive, or a sign-in that rotates would discard the state the
// flow was in the middle of.
func TestRotationCarriesTheSessionAcross(t *testing.T) {
	m := NewMemorySessions("cloud_session_id", time.Hour)
	old, _ := start(t, m, "")
	_ = old.Set("user", "alice")
	_ = old.Set("returnTo", "/console")

	fresh, err := m.RegenerateID(httptest.NewRecorder(), old)
	if err != nil {
		t.Fatalf("RegenerateID: %v", err)
	}
	if got := fresh.Get("user"); got != "alice" {
		t.Fatalf("user after rotation = %v, want alice", got)
	}
	if got := fresh.Get("returnTo"); got != "/console" {
		t.Fatalf("returnTo after rotation = %v, want /console", got)
	}
}

// The replacement cookie must not be weaker than the one it replaces. A rotation
// that dropped HttpOnly would hand back exactly what rotation exists to protect.
func TestRotationWritesACookieNoWeakerThanTheFirst(t *testing.T) {
	m := NewMemorySessions("cloud_session_id", time.Hour)

	first := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	s, err := m.SessionStart(first, r)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	issued := setCookie(t, m, first)
	if issued == nil {
		t.Fatal("SessionStart wrote no cookie")
	}

	rec := httptest.NewRecorder()
	fresh, err := m.RegenerateID(rec, s)
	if err != nil {
		t.Fatalf("RegenerateID: %v", err)
	}
	rotated := setCookie(t, m, rec)
	if rotated == nil {
		t.Fatal("rotation wrote no cookie, so the browser still presents the retired id")
	}
	if rotated.Value != fresh.SessionID() {
		t.Fatalf("cookie value %q is not the new session id %q", rotated.Value, fresh.SessionID())
	}
	if !rotated.HttpOnly || rotated.Path != issued.Path || rotated.SameSite != issued.SameSite {
		t.Fatalf("rotated cookie is weaker than the issued one: %+v vs %+v", rotated, issued)
	}
	if rotated.MaxAge <= 0 {
		t.Fatalf("rotated cookie MaxAge = %d, want the session lifetime", rotated.MaxAge)
	}
}

// Sign-out expires the cookie, so the browser stops presenting a handle to a
// session that no longer exists.
func TestClearCookieExpiresIt(t *testing.T) {
	m := NewMemorySessions("cloud_session_id", time.Hour)
	rec := httptest.NewRecorder()
	m.ClearCookie(rec)

	c := setCookie(t, m, rec)
	if c == nil {
		t.Fatal("ClearCookie wrote no cookie, so the browser keeps the old one")
	}
	if c.MaxAge >= 0 {
		t.Fatalf("MaxAge = %d, want a negative value so the browser deletes it", c.MaxAge)
	}
	if c.Value != "" {
		t.Fatalf("value = %q, want empty", c.Value)
	}
}

// Rotation runs on the sign-in path, where a caller may arrive with no session at
// all. It must mint one rather than panic on a nil store.
func TestRotationWithNoPriorSession(t *testing.T) {
	m := NewMemorySessions("cloud_session_id", time.Hour)
	fresh, err := m.RegenerateID(httptest.NewRecorder(), nil)
	if err != nil {
		t.Fatalf("RegenerateID(nil): %v", err)
	}
	if fresh == nil || fresh.SessionID() == "" {
		t.Fatal("rotation with no prior session produced no session")
	}
}
