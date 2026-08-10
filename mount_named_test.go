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

package ai

// PROMOTION: an address in [promoted] is a REAL route on the host's router,
// reaching the SAME handler the /v1/* glob reaches.
//
// Three properties, and they only mean something together. Naming an address
// without changing behaviour is the whole point: if the request took a different
// path, this would be a SECOND implementation of /v1/models rather than a
// description of the one that already exists.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/ai/routers"
	"github.com/zap-proto/zip"
)

// TestPromotedAddressesExistInTheRouteTable refuses a promotion of an address
// this service does not serve. That is the whole failure mode of a hand-written
// list next to a generated table: the list drifts, the host publishes an address
// nothing answers, and the lie reaches eight SDKs. The list carries patterns
// only — the verbs come from the live table — so this is the one way it can be
// wrong.
func TestPromotedAddressesExistInTheRouteTable(t *testing.T) {
	if len(promoted) == 0 {
		t.Fatal("promoted is empty — the seam exists to name addresses and names none")
	}
	patterns := routers.App.Patterns()
	for _, pattern := range promoted {
		if len(patterns[pattern]) == 0 {
			t.Errorf("promoted names %q and the route table does not register it — "+
				"the host would publish an address this service never answers", pattern)
		}
	}
}

// TestPromotedAddressesAreRoutesOnTheHostRouter proves the host router holds each
// promoted address at its own pattern, which is what makes it describable. Before
// this they existed only inside the controller handler and the host could publish
// nothing but /v1/{wildcard1}.
func TestPromotedAddressesAreRoutesOnTheHostRouter(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	routes(app)

	held := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		held[r.Method+" "+r.Path] = true
	}

	patterns := routers.App.Patterns()
	for _, pattern := range promoted {
		for _, method := range patterns[pattern] {
			switch method {
			case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			default:
				continue // no host registrar; still reaches the glob, as before
			}
			if !held[method+" "+pattern] {
				t.Errorf("%s %s is promoted and the host router does not hold it — "+
					"the address still reaches the wire as /v1/{wildcard1}", method, pattern)
			}
		}
	}

	// The addresses this landed for, named so that dropping one is a decision
	// somebody makes on purpose rather than a silent loss: each is a published SDK
	// method, an MCP tool and a CLI command.
	for _, want := range []string{
		"GET /v1/models",
		"GET /v1/models/providers",
		"GET /v1/models/:model/access",
		"POST /v1/models/:model/access",
	} {
		if !held[want] {
			t.Errorf("%s is not a route on the host router", want)
		}
	}
}

// TestPromotedAddressesReachTheSameHandler proves promotion changed the
// DESCRIPTION and nothing else: the request arrives at the controller handler at its
// original path and verb, exactly as the glob delivered it. A promoted route that
// took its own path would be a second implementation — the defect, not the fix.
func TestPromotedAddressesReachTheSameHandler(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	routes(app)

	var sawPath, sawMethod string
	SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath, sawMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer SetHandler(nil)

	for _, tc := range []struct{ method, url string }{
		{http.MethodGet, "/v1/models"},
		{http.MethodGet, "/v1/models/providers"},
		{http.MethodGet, "/v1/models/enso/access"},
		{http.MethodPost, "/v1/models/enso/access"},
		// The glob still carries everything that is not promoted.
		{http.MethodPost, "/v1/chat/completions"},
	} {
		sawPath, sawMethod = "", ""
		resp, err := app.Fiber().Test(httptest.NewRequest(tc.method, tc.url, nil))
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.url, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s: status=%d want 200", tc.method, tc.url, resp.StatusCode)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		if strings.TrimSpace(string(body)) != `{"ok":true}` {
			t.Errorf("%s %s: body=%q — did not reach the runtime handler", tc.method, tc.url, body)
		}
		if sawPath != tc.url || sawMethod != tc.method {
			t.Errorf("%s %s: handler saw %s %s — the request was rewritten",
				tc.method, tc.url, sawMethod, sawPath)
		}
	}
}

// TestPromotedAddressesCarryTheirSentence proves a promoted address has the third
// thing a published operation owes a reader. Address, verb and sentence all come
// from [routers.Document], the one accessor: it reads the live router for
// membership and lays the handler's own doc comment over it. Promoting an address
// with no sentence would publish an operation no generated client can explain.
func TestPromotedAddressesCarryTheirSentence(t *testing.T) {
	paths, _ := routers.Document()["paths"].(map[string]any)
	for _, pattern := range promoted {
		// The document keys the path in OpenAPI's {name} spelling.
		open := pattern
		for _, seg := range strings.Split(pattern, "/") {
			if strings.HasPrefix(seg, ":") {
				open = strings.Replace(open, seg, "{"+seg[1:]+"}", 1)
			}
		}
		item, ok := paths[open].(map[string]any)
		if !ok {
			t.Errorf("%s is promoted and the document does not carry it", open)
			continue
		}
		for verb, o := range item {
			op, _ := o.(map[string]any)
			if s, _ := op["summary"].(string); s != "" {
				continue
			}
			t.Errorf("%s %s is promoted and the document has no sentence for it — "+
				"a published operation nobody can explain", strings.ToUpper(verb), open)
		}
	}
}
