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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	web "github.com/hanzoai/ai/web"
)

// There is no zap_finetune.go, and this file is the reason. Every other
// /v1 group that stayed off the native registry stayed off it for a REASON, and
// a reason nobody wrote down decays into "we never got to it" — which is an
// invitation to convert. These tests hold the two mechanical facts that make
// /v1/finetune/* the wrong shape for the registry, so the day someone tries, the
// build tells them before the wire does.
//
//  1. The body-only registry is keyed on PATH ALONE. /v1/finetune/jobs answers
//     GET (list) and POST (create); one registration necessarily answers both.
//  2. The group authenticates on the CONSOLE SESSION COOKIE, and no cookie
//     crosses the ZAP gateway — serveGatewayViaRouter forwards Authorization and
//     nothing else. Every one of these ops is 401 over ZAP today, so there is no
//     working native surface to make faster.

// finetuneOps mirrors routers/finetune_router.go: 8 registered paths, 9
// operations. Naming each handler as a method expression means a rename or a
// deletion breaks THIS FILE at compile time rather than rotting quietly.
var finetuneOps = []struct {
	method, path, query string
	call                func(*ApiController)
}{
	{"GET", "/v1/finetune/jobs", "", (*ApiController).ListFinetuneJobs},
	{"POST", "/v1/finetune/jobs", "", (*ApiController).CreateFinetuneJob},
	{"GET", "/v1/finetune/job", "id=acme/j1", (*ApiController).GetFinetuneJob},
	{"POST", "/v1/finetune/cancel", "id=acme/j1", (*ApiController).CancelFinetuneJob},
	{"POST", "/v1/finetune/deploy", "id=acme/j1", (*ApiController).DeployFinetuneJob},
	{"GET", "/v1/finetune/presets", "baseModel=zen&method=lora", (*ApiController).GetFinetunePresets},
	{"GET", "/v1/finetune/hf/models", "q=llama&limit=5", (*ApiController).SearchHfModels},
	{"GET", "/v1/finetune/hf/datasets", "q=alpaca&limit=5", (*ApiController).SearchHfDatasets},
	{"GET", "/v1/finetune/hf/repo", "id=zenlm/zen&kind=model", (*ApiController).GetHfRepo},
}

// No group claims a finetune path in either gateway registry. This is the
// measurement the decision rests on, pinned so a later registration cannot make
// the recorded reasoning silently false.
func TestFinetuneHasNoNativeZapClaim(t *testing.T) {
	for _, op := range finetuneOps {
		if _, ok := lookupGatewayRoute(op.path); ok {
			t.Errorf("%s: an HTTP-shaped registry entry now claims it — the refusal recorded in routers/finetune_router.go is stale", op.path)
		}
		if _, ok := lookupGatewayHandler(op.path); ok {
			t.Errorf("%s: a body-only registry entry now claims it — see reason 1, it cannot carry this group", op.path)
		}
	}
}

// What actually serves these over ZAP is the fallback, and it arrives with the
// full request line — the method and the query string a body-only zapHandler
// (ctx, auth, body) has no argument for. Seven of the nine ops read the query;
// only CreateFinetuneJob reads a body.
func TestFinetuneReachesTheRouterWithItsRequestLine(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	})

	for _, op := range finetuneOps {
		gotMethod, gotPath, gotQuery = "", "", ""
		_, handled, err := dispatchGateway(context.Background(), router, op.method, op.path, op.query, "Bearer t", nil)
		if err != nil {
			t.Fatalf("%s %s: dispatch: %v", op.method, op.path, err)
		}
		if !handled {
			t.Fatalf("%s %s: not handled — nothing serves it", op.method, op.path)
		}
		if gotMethod != op.method {
			t.Errorf("%s %s: method = %q — the verb is lost", op.method, op.path, gotMethod)
		}
		if gotPath != op.path {
			t.Errorf("%s %s: path = %q", op.method, op.path, gotPath)
		}
		if gotQuery != op.query {
			t.Errorf("%s %s: query = %q, want %q — the request's only input is lost", op.method, op.path, gotQuery, op.query)
		}
	}
}

// Reason 1, mechanically: the body-only registry's key space has no verb
// dimension. One prefix matches the list op and the create op identically, so a
// single registerGatewayPath("/v1/finetune/jobs", h) would route POST-create
// traffic into whichever handler h happens to be. A wrong handler, not an error
// — the same failure zap_gateway_fallback.go was written to stop.
func TestFinetuneJobsMultiplexesTwoVerbsAtOnePath(t *testing.T) {
	var list, create struct{ method, path string }
	for _, op := range finetuneOps {
		if op.path != "/v1/finetune/jobs" {
			continue
		}
		if op.method == "GET" {
			list.method, list.path = op.method, op.path
		} else {
			create.method, create.path = op.method, op.path
		}
	}
	if list.path == "" || create.path == "" {
		t.Fatal("expected /v1/finetune/jobs to carry both a GET and a POST op")
	}
	if list.path != create.path {
		t.Fatalf("paths diverged: %q vs %q", list.path, create.path)
	}
	const prefix = "/v1/finetune/jobs"
	if !prefixMatch(prefix, list.path) || !prefixMatch(prefix, create.path) {
		t.Fatal("prefixMatch must claim both — that IS the collision")
	}

	// The near miss that makes the group look safer than it is: /v1/finetune/job
	// is a real sibling path, and it must not swallow /v1/finetune/jobs. Longest
	// -prefix matching gets this right only because prefixMatch requires a
	// segment boundary; drop that and the singular route eats the plural one.
	if prefixMatch("/v1/finetune/job", "/v1/finetune/jobs") {
		t.Error("/v1/finetune/job must not claim /v1/finetune/jobs — segment boundary lost")
	}
}

// Reason 2: the credential these ops require does not cross the gateway. The
// bridged request carries Authorization, Content-Type and Accept — no Cookie —
// and the group authenticates with RequireSignedInUser (session), not
// RequirePrincipal (session OR bearer). So over ZAP every op is 401 regardless
// of which registry serves it. Converting the group would mean inventing a
// bearer auth path the HTTP route does not have: two auth answers for one
// surface.
func TestFinetuneCredentialDoesNotCrossTheGateway(t *testing.T) {
	var gotCookie, gotAuth string
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie, gotAuth = r.Header.Get("Cookie"), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})

	if _, _, err := dispatchGateway(context.Background(), router, "GET", "/v1/finetune/jobs", "", "Bearer t", nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if gotAuth != "Bearer t" {
		t.Errorf("Authorization = %q, want the forwarded credential", gotAuth)
	}
	if gotCookie != "" {
		t.Errorf("Cookie = %q — a session now crosses the gateway, so reason 2 is stale", gotCookie)
	}

	// And that is fatal for this group: a gateway-shaped request (bearer, no
	// cookie) is refused by every op before it does any work.
	for _, op := range finetuneOps {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(op.method, op.path+"?"+op.query, nil)
		req.Header.Set("Authorization", "Bearer t")
		req.Header.Set("Accept", "application/json")
		ctx := web.NewContext()
		ctx.Reset(rec, req)
		c := &ApiController{}
		c.Init(ctx, "ApiController", "Finetune", nil)

		op.call(c)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401 — this group is reachable over ZAP after all, so revisit the refusal",
				op.method, op.path, rec.Code)
		}
	}
}
