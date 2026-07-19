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
	"reflect"
	"strings"
	"testing"
)

// dispatchLog records lifecycle events across the freshly-instantiated
// controllers a test drives; reset it at the start of each test.
var dispatchLog []string

type probeController struct {
	Controller
}

func (c *probeController) Prepare() { dispatchLog = append(dispatchLog, "prepare") }
func (c *probeController) Finish()  { dispatchLog = append(dispatchLog, "finish") }

func (c *probeController) Hello() {
	dispatchLog = append(dispatchLog, "hello:"+c.Ctx.Input.Param("name"))
	c.Ctx.Output.Body([]byte("hi"))
}

func (c *probeController) Create() {
	dispatchLog = append(dispatchLog, "create")
	c.Ctx.Output.SetStatus(http.StatusCreated)
	c.Ctx.Output.Body([]byte("created"))
}

func (c *probeController) Abort() {
	dispatchLog = append(dispatchLog, "abort")
	c.StopRun()
	dispatchLog = append(dispatchLog, "unreached")
}

func (c *probeController) Boom() {
	dispatchLog = append(dispatchLog, "boom")
	panic("kaboom")
}

func serve(r *Router, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// TestRouteParamDispatch proves a :param route captures the segment and the
// mapped method runs on a fresh controller.
func TestRouteParamDispatch(t *testing.T) {
	dispatchLog = nil
	r := NewRouter()
	r.Router("/v1/greet/:name", &probeController{}, "GET:Hello")

	rec := serve(r, "GET", "/v1/greet/world")
	if rec.Body.String() != "hi" {
		t.Fatalf("body = %q, want hi", rec.Body.String())
	}
	if want := []string{"prepare", "hello:world", "finish"}; !reflect.DeepEqual(dispatchLog, want) {
		t.Fatalf("dispatch log = %v, want %v", dispatchLog, want)
	}
}

// TestMethodMapping proves the verb selects the handler, an unmapped verb is
// 405, and an unknown path is 404.
func TestMethodMapping(t *testing.T) {
	r := NewRouter()
	r.Router("/v1/items", &probeController{}, "GET:Hello;POST:Create")

	if rec := serve(r, "POST", "/v1/items"); rec.Code != http.StatusCreated || rec.Body.String() != "created" {
		t.Fatalf("POST = (%d, %q), want (201, created)", rec.Code, rec.Body.String())
	}
	if rec := serve(r, "GET", "/v1/items"); rec.Body.String() != "hi" {
		t.Fatalf("GET body = %q, want hi", rec.Body.String())
	}
	if rec := serve(r, "DELETE", "/v1/items"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE = %d, want 405", rec.Code)
	}
	if rec := serve(r, "GET", "/v1/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", rec.Code)
	}
}

// TestFreshInstancePerRequest proves handler state does not leak between
// requests: each dispatch reflects a new controller value.
func TestFreshInstancePerRequest(t *testing.T) {
	dispatchLog = nil
	r := NewRouter()
	r.Router("/v1/greet/:name", &probeController{}, "GET:Hello")
	serve(r, "GET", "/v1/greet/a")
	serve(r, "GET", "/v1/greet/b")
	want := []string{"prepare", "hello:a", "finish", "prepare", "hello:b", "finish"}
	if !reflect.DeepEqual(dispatchLog, want) {
		t.Fatalf("log = %v, want %v", dispatchLog, want)
	}
}

// TestFilterShortCircuit proves a BeforeRouter filter that writes the response
// stops the chain: the handler never runs (an auth reject).
func TestFilterShortCircuit(t *testing.T) {
	dispatchLog = nil
	r := NewRouter()
	r.Router("/v1/items", &probeController{}, "GET:Hello")
	r.InsertFilter("*", BeforeRouter, func(ctx *Context) {
		dispatchLog = append(dispatchLog, "auth")
		ctx.Output.SetStatus(http.StatusUnauthorized)
		ctx.Output.Body([]byte("denied"))
	})

	rec := serve(r, "GET", "/v1/items")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	for _, e := range dispatchLog {
		if strings.HasPrefix(e, "hello") {
			t.Fatalf("handler ran despite short-circuit: %v", dispatchLog)
		}
	}
	if dispatchLog[0] != "auth" {
		t.Fatalf("filter did not run first: %v", dispatchLog)
	}
}

// TestFilterOrderAndAfterExec proves BeforeRouter filters run in registration
// order before the handler, and an AfterExec filter runs after Finish.
func TestFilterOrderAndAfterExec(t *testing.T) {
	dispatchLog = nil
	r := NewRouter()
	r.Router("/v1/items", &probeController{}, "GET:Hello")
	r.InsertFilter("*", BeforeRouter, func(*Context) { dispatchLog = append(dispatchLog, "f1") })
	r.InsertFilter("*", BeforeRouter, func(*Context) { dispatchLog = append(dispatchLog, "f2") })
	r.InsertFilter("*", AfterExec, func(*Context) { dispatchLog = append(dispatchLog, "after") }, false)

	serve(r, "GET", "/v1/items")
	want := []string{"f1", "f2", "prepare", "hello:", "finish", "after"}
	if !reflect.DeepEqual(dispatchLog, want) {
		t.Fatalf("log = %v, want %v", dispatchLog, want)
	}
}

// TestAfterExecReturnOnOutputFalse proves an AfterExec filter registered with
// returnOnOutput=false still runs after the handler wrote the response.
func TestAfterExecReturnOnOutputFalse(t *testing.T) {
	dispatchLog = nil
	r := NewRouter()
	r.Router("/v1/items", &probeController{}, "GET:Hello") // Hello writes a body
	r.InsertFilter("*", AfterExec, func(*Context) { dispatchLog = append(dispatchLog, "record") }, false)

	serve(r, "GET", "/v1/items")
	if len(dispatchLog) == 0 || dispatchLog[len(dispatchLog)-1] != "record" {
		t.Fatalf("AfterExec(false) must run after a started response: %v", dispatchLog)
	}
}

// TestFilterPrefixPattern proves a "/prefix/*" filter matches by prefix and
// leaves other paths untouched.
func TestFilterPrefixPattern(t *testing.T) {
	dispatchLog = nil
	r := NewRouter()
	r.Router("/v1/cloud/x", &probeController{}, "GET:Hello")
	r.Router("/v1/other", &probeController{}, "GET:Hello")
	r.InsertFilter("/v1/cloud/*", BeforeRouter, func(*Context) { dispatchLog = append(dispatchLog, "cloud") })

	serve(r, "GET", "/v1/cloud/x")
	serve(r, "GET", "/v1/other")
	got := strings.Join(dispatchLog, ",")
	if strings.Count(got, "cloud") != 1 {
		t.Fatalf("cloud filter must run once (only for the prefix): %v", dispatchLog)
	}
}

// TestStopRunRunsFinish proves a StopRun abort unwinds the handler but Finish
// still runs and the request does not crash.
func TestStopRunRunsFinish(t *testing.T) {
	dispatchLog = nil
	r := NewRouter()
	r.Router("/v1/abort", &probeController{}, "GET:Abort")

	serve(r, "GET", "/v1/abort")
	want := []string{"prepare", "abort", "finish"}
	if !reflect.DeepEqual(dispatchLog, want) {
		t.Fatalf("log = %v, want %v (no 'unreached', Finish present)", dispatchLog, want)
	}
}

// TestHandlerPanicRecovers proves a non-abort panic becomes a 500 rather than
// crashing the server goroutine.
func TestHandlerPanicRecovers(t *testing.T) {
	dispatchLog = nil
	r := NewRouter()
	r.Router("/v1/boom", &probeController{}, "GET:Boom")

	rec := serve(r, "GET", "/v1/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic recovery code = %d, want 500", rec.Code)
	}
}

// TestSplitPath covers path segmentation.
func TestSplitPath(t *testing.T) {
	cases := map[string][]string{
		"/":               {},
		"":                {},
		"/v1/items":       {"v1", "items"},
		"/v1/greet/:name": {"v1", "greet", ":name"},
		"v1/x/":           {"v1", "x"},
		"/v1//items":      {"v1", "items"}, // empty segment
		"/v1/./items":     {"v1", "items"}, // dot
		"/v1/a/../items":  {"v1", "items"}, // dot-dot
		"/v1/../items":    {"items"},       // dot-dot to root
	}
	for in, want := range cases {
		if got := splitPath(in); !reflect.DeepEqual(got, want) {
			t.Fatalf("splitPath(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestFilterRewritesRoutingPath is the HIGH-1 regression: a BeforeRouter
// filter that mutates Request.URL.Path (as the /v1/cloud and /v1/iam rewrites
// do) must route to the NEW path. Only the rewritten target is registered —
// mirroring /v1/cloud/*, which has no direct route and depends entirely on
// the rewrite.
func TestFilterRewritesRoutingPath(t *testing.T) {
	dispatchLog = nil
	r := NewRouter()
	r.Router("/v1/items", &probeController{}, "GET:Hello")
	r.InsertFilter("/v1/cloud/*", BeforeRouter, func(ctx *Context) {
		ctx.Request.URL.Path = "/v1/items"
	})

	rec := serve(r, "GET", "/v1/cloud/items")
	if rec.Code != http.StatusOK || rec.Body.String() != "hi" {
		t.Fatalf("rewritten path must route to /v1/items; got code=%d body=%q", rec.Code, rec.Body.String())
	}
}

// TestRoutingCleansPath is the I-1 regression: paths with empty, dot or
// dot-dot segments route to the cleaned target instead of 404ing.
func TestRoutingCleansPath(t *testing.T) {
	r := NewRouter()
	r.Router("/v1/items", &probeController{}, "GET:Hello")
	for _, p := range []string{"/v1//items", "/v1/./items", "/v1/x/../items"} {
		rec := serve(r, "GET", p)
		if rec.Code != http.StatusOK || rec.Body.String() != "hi" {
			t.Fatalf("path %q must clean-route to /v1/items; got code=%d body=%q", p, rec.Code, rec.Body.String())
		}
	}
}

// TestParseMapping covers the method mapping grammar.
func TestParseMapping(t *testing.T) {
	got := parseMapping("GET:List;post:Create ; *:Any")
	want := map[string]string{"GET": "List", "POST": "Create", "*": "Any"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMapping = %v, want %v", got, want)
	}
}

// TestMatchFilterPattern covers wildcard, prefix and exact filter patterns.
func TestMatchFilterPattern(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*", "/anything", true},
		{"/*", "/anything", true},
		{"/v1/cloud/*", "/v1/cloud/x", true},
		{"/v1/cloud/*", "/v1/iam/x", false},
		{"/v1/exact", "/v1/exact", true},
		{"/v1/exact", "/v1/other", false},
	}
	for _, c := range cases {
		if got := matchFilterPattern(c.pattern, c.path); got != c.want {
			t.Fatalf("matchFilterPattern(%q,%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// TestInsertFilterInvalidPosition proves an out-of-range position is rejected.
func TestInsertFilterInvalidPosition(t *testing.T) {
	r := NewRouter()
	if err := r.InsertFilter("*", 99, func(*Context) {}); err == nil {
		t.Fatal("InsertFilter must reject an invalid position")
	}
}

// recordingStore notes when its session was released.
type recordingStore struct {
	*memStore
	released bool
}

func (s *recordingStore) SessionRelease(http.ResponseWriter) { s.released = true }

type fakeSessions struct{ store *recordingStore }

func (f *fakeSessions) SessionStart(http.ResponseWriter, *http.Request) (Store, error) {
	return f.store, nil
}

type sessionProbe struct{ Controller }

func (c *sessionProbe) Who() {
	u, _ := c.GetSession("user").(string)
	c.Ctx.Output.Body([]byte(u))
}

// TestSessionHookBindsAndReleases proves UseSessions binds the store to the
// request (the controller reads it) and releases it after the response — the
// I-4 session-binding mechanism 4b wires to the session provider.
func TestSessionHookBindsAndReleases(t *testing.T) {
	store := &recordingStore{memStore: newMemStore()}
	store.Set("user", "alice")

	r := NewRouter()
	r.UseSessions(&fakeSessions{store: store})
	r.Router("/v1/whoami", &sessionProbe{}, "GET:Who")

	rec := serve(r, "GET", "/v1/whoami")
	if rec.Body.String() != "alice" {
		t.Fatalf("controller must read the bound session; got %q", rec.Body.String())
	}
	if !store.released {
		t.Fatal("SessionRelease must run after the response")
	}
}

// TestNoSessionManagerLeavesStoreNil proves that without a manager the session
// store stays nil and the fail-secure accessors return no value.
func TestNoSessionManagerLeavesStoreNil(t *testing.T) {
	r := NewRouter()
	r.Router("/v1/whoami", &sessionProbe{}, "GET:Who")
	rec := serve(r, "GET", "/v1/whoami")
	if rec.Body.String() != "" {
		t.Fatalf("no session manager must yield empty session read; got %q", rec.Body.String())
	}
}
