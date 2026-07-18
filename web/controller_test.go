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
)

// memStore is an in-memory Store for session tests.
type memStore struct {
	m  map[interface{}]interface{}
	id string
}

func newMemStore() *memStore { return &memStore{m: map[interface{}]interface{}{}, id: "sid-1"} }

func (s *memStore) Set(k, v interface{}) error           { s.m[k] = v; return nil }
func (s *memStore) Get(k interface{}) interface{}        { return s.m[k] }
func (s *memStore) Delete(k interface{}) error           { delete(s.m, k); return nil }
func (s *memStore) SessionID() string                    { return s.id }
func (s *memStore) SessionRelease(w http.ResponseWriter) {}
func (s *memStore) Flush() error                         { s.m = map[interface{}]interface{}{}; return nil }

// initController builds a Controller bound to a fresh context for a request.
func initController(r *http.Request) (*Controller, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	ctx := NewContext()
	ctx.Reset(rec, r)
	c := &Controller{}
	c.Init(ctx, "Test", "Action", c)
	return c, rec
}

// TestInitAliasesData proves Init binds Ctx and that c.Data is the same map as
// the Input data: a value set through Ctx.Input.SetData is visible on c.Data,
// which the Finish/error-log path relies on.
func TestInitAliasesData(t *testing.T) {
	c, _ := initController(httptest.NewRequest("GET", "/", nil))
	if c.Ctx == nil {
		t.Fatal("Init must set Ctx")
	}
	c.Ctx.Input.SetData("startTime", 7)
	if c.Data["startTime"] != 7 {
		t.Fatal("c.Data must alias the Input data map")
	}
	c.Data["json"] = "x"
	if c.Ctx.Input.GetData("json") != "x" {
		t.Fatal("writes to c.Data must be visible via Input.GetData")
	}
}

// TestInputAndGetString proves Input() returns parsed form values and
// GetString reads a route param or query value, falling back to a default.
func TestInputAndGetString(t *testing.T) {
	c, _ := initController(httptest.NewRequest("GET", "/?owner=acme", nil))
	if c.Input().Get("owner") != "acme" {
		t.Fatalf("Input()[owner] = %q, want acme", c.Input().Get("owner"))
	}
	if v := c.GetString("owner"); v != "acme" {
		t.Fatalf("GetString(owner) = %q, want acme", v)
	}
	if v := c.GetString("missing", "def"); v != "def" {
		t.Fatalf("GetString default = %q, want def", v)
	}
}

// TestServeJSON proves ServeJSON writes c.Data["json"] as the JSON body.
func TestServeJSON(t *testing.T) {
	c, rec := initController(httptest.NewRequest("GET", "/", nil))
	c.Data["json"] = map[string]string{"status": "ok"}
	c.ServeJSON()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("ServeJSON wrote no body")
	}
}

// TestSessionMethods proves the session accessors read and write through a
// bound store, taking it from Input.CruSession on first use.
func TestSessionMethods(t *testing.T) {
	c, _ := initController(httptest.NewRequest("GET", "/", nil))
	c.Ctx.Input.CruSession = newMemStore()

	c.SetSession("user", "alice")
	if v := c.GetSession("user"); v != "alice" {
		t.Fatalf("GetSession = %v, want alice", v)
	}
	if c.StartSession() == nil {
		t.Fatal("StartSession must return the bound store")
	}
	c.DelSession("user")
	if v := c.GetSession("user"); v != nil {
		t.Fatalf("after DelSession GetSession = %v, want nil", v)
	}
}

// TestSessionNilSafe proves the accessors are fail-secure with no store bound:
// GetSession returns nil and SetSession/DelSession no-op rather than panicking
// (beego dereferences a nil store here — a documented crash this avoids).
func TestSessionNilSafe(t *testing.T) {
	c, _ := initController(httptest.NewRequest("GET", "/", nil))
	if v := c.GetSession("user"); v != nil {
		t.Fatalf("GetSession with no store = %v, want nil", v)
	}
	c.SetSession("user", "x") // must not panic
	c.DelSession("user")      // must not panic
}

// TestStopRunPanicsErrAbort proves StopRun raises the recoverable ErrAbort.
func TestStopRunPanicsErrAbort(t *testing.T) {
	c, _ := initController(httptest.NewRequest("GET", "/", nil))
	defer func() {
		if r := recover(); r != ErrAbort {
			t.Fatalf("StopRun panicked with %v, want ErrAbort", r)
		}
	}()
	c.StopRun()
	t.Fatal("StopRun must panic")
}
