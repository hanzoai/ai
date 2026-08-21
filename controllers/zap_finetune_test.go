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
	"testing"
)

// There is no zap_finetune.go, and this file is the reason.
//
// A group that stayed off the native registry for a reason nobody wrote down
// decays into "we just never got to it", which reads as an invitation. These
// tests hold the two facts that make /v1/ai/finetune/* the wrong shape for the
// registry, so the day someone tries to convert it the build says so first.
//
//  1. The body-only registry is keyed on PATH ALONE — registerGatewayPath takes a
//     zapHandler(ctx, auth, body), which has no argument for the verb or the
//     query. /v1/ai/finetune/jobs answers GET (list) AND POST (create), so one
//     registration necessarily answers both; and seven of the nine ops carry
//     their entire input in the query string, which that handler never sees.
//  2. The group authenticates on the CONSOLE SESSION COOKIE
//     (RequireSignedInUser, nine times in controllers/finetune.go), and no
//     cookie crosses the ZAP gateway: serveGatewayViaRouter forwards
//     Authorization, Content-Type and Accept, full stop. Every op is 401 over
//     ZAP today — there is no working native surface to make faster.
//
// Deliberately built on the registry lookups and the handlers themselves, not on
// the MsgType 200 dispatcher: what is being pinned is a property of the finetune
// group, and the bridge has its own tests.

// finetuneOps mirrors routers/finetune_router.go: 8 registered paths, 9
// operations. Naming each handler as a method expression means a rename or a
// deletion breaks THIS FILE at compile time instead of rotting quietly.
var finetuneOps = []struct {
	method, path, query string
	call                func(*ApiController)
}{
	{"GET", "/v1/ai/finetune/jobs", "", (*ApiController).ListFinetuneJobs},
	{"POST", "/v1/ai/finetune/jobs", "", (*ApiController).CreateFinetuneJob},
	{"GET", "/v1/ai/finetune/job", "id=acme/j1", (*ApiController).GetFinetuneJob},
	{"POST", "/v1/ai/finetune/cancel", "id=acme/j1", (*ApiController).CancelFinetuneJob},
	{"POST", "/v1/ai/finetune/deploy", "id=acme/j1", (*ApiController).DeployFinetuneJob},
	{"GET", "/v1/ai/finetune/presets", "baseModel=zen&method=lora", (*ApiController).GetFinetunePresets},
	{"GET", "/v1/ai/finetune/hf/models", "q=llama&limit=5", (*ApiController).SearchHfModels},
	{"GET", "/v1/ai/finetune/hf/datasets", "q=alpaca&limit=5", (*ApiController).SearchHfDatasets},
	{"GET", "/v1/ai/finetune/hf/repo", "id=zenlm/zen&kind=model", (*ApiController).GetHfRepo},
}

// No group claims a finetune path in either gateway registry. This is the
// measurement the decision rests on, pinned so a later registration cannot make
// the recorded reasoning silently false.
func TestFinetuneHasNoNativeZapClaim(t *testing.T) {
	for _, op := range finetuneOps {
		if len(lookupGatewayRoutes(op.path)) > 0 {
			t.Errorf("%s: an HTTP-shaped registry entry now claims it — the refusal recorded in routers/finetune_router.go is stale", op.path)
		}
		if _, ok := lookupGatewayHandler(op.path); ok {
			t.Errorf("%s: a body-only registry entry now claims it — see reason 1: that registry cannot carry this group", op.path)
		}
	}
}

// Reason 1, mechanically: the body-only registry's key space has no verb
// dimension. One prefix claims the list op and the create op identically, so a
// single registerGatewayPath("/v1/ai/finetune/jobs", h) routes POST-create traffic
// into whichever handler h happens to be — a wrong handler, not an error. That
// is the exact failure zap_gateway_fallback.go was written to stop.
func TestFinetuneJobsMultiplexesTwoVerbsAtOnePath(t *testing.T) {
	verbs := map[string]bool{}
	for _, op := range finetuneOps {
		if op.path == "/v1/ai/finetune/jobs" {
			verbs[op.method] = true
		}
	}
	if !verbs["GET"] || !verbs["POST"] {
		t.Fatalf("expected /v1/ai/finetune/jobs to carry both GET and POST, got %v", verbs)
	}
	// Both ops live at one path, and prefixMatch — all the body-only registry
	// consults — cannot tell them apart.
	if !prefixMatch("/v1/ai/finetune/jobs", "/v1/ai/finetune/jobs") {
		t.Fatal("prefixMatch must claim the path it is keyed on")
	}

	// The near miss that makes the group look safer than it is: /v1/ai/finetune/job
	// is a real sibling, and it must not swallow /v1/ai/finetune/jobs. Longest-prefix
	// matching only gets this right because prefixMatch demands a segment
	// boundary; lose that and the singular route eats the plural one.
	if prefixMatch("/v1/ai/finetune/job", "/v1/ai/finetune/jobs") {
		t.Error("/v1/ai/finetune/job must not claim /v1/ai/finetune/jobs — the segment boundary is gone")
	}
}

// Reason 2: the credential these ops require does not cross the gateway. A
// request bridged from ZAP carries Authorization, Content-Type and Accept — no
// Cookie — and this group authenticates with RequireSignedInUser (session), not
// RequirePrincipal (session OR bearer). So it is 401 over ZAP no matter which
// registry serves it, and converting the group would mean inventing a bearer
// path the HTTP route does not have: two auth answers for one surface.
func TestFinetuneRefusesTheGatewayShapedRequest(t *testing.T) {
	for _, op := range finetuneOps {
		target := op.path
		if op.query != "" {
			target += "?" + op.query
		}
		// A bearer and no session user — what a bridged request always is.
		c := presenting(visit(op.method, target), "Bearer t")

		op.call(c)

		if answered(c) != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401 — this group is reachable over ZAP after all, so the refusal needs revisiting",
				op.method, op.path, answered(c))
		}
	}
}
