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

// Tests for the gateway dispatch chain (zap_registry.go + dispatchGateway).
//
// A registered prefix is a claim on a SUBTREE, and a subtree holds siblings that
// belong to other registrations. "/v1/models/" carries the {model}/access routes;
// "/v1/models/providers" is a different endpoint with its own handler. Dispatch
// therefore cannot stop at the first prefix that matches — it runs the candidates
// longest-first and takes the first that CLAIMS the path. errDecline is how a
// handler says "not mine", and these tests pin every link of that chain.

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// TestGatewayDeclineReachesSibling is the regression for a live 404.
//
// "/v1/models/" is registered as an HTTP-shaped route and it prefix-matches every
// path below it, so it won the lookup for /v1/models/providers — whose own
// handler sits one line away in the body-only registry. dispatchGateway consulted
// the HTTP-shaped registry first and reported the request handled
// unconditionally, so the sibling handler was unreachable and the endpoint
// answered
//
//	404 {"status":"error","msg":"not found: /v1/models/providers"}
//
// in production. With the decline seam the access handler steps aside and the
// providers handler answers.
func TestGatewayDeclineReachesSibling(t *testing.T) {
	msg, handled, err := dispatchGateway(context.Background(), nil, "GET", "/v1/models/providers", "", "", nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !handled {
		t.Fatal("/v1/models/providers not handled — a registered endpoint fell off the chain")
	}
	status, body := gwResp(t, msg)
	if status != 200 {
		t.Fatalf("status = %d msg=%q, want 200 — the /v1/models/ prefix is still swallowing its sibling", status, body.Msg)
	}
	if body.Status != "ok" {
		t.Fatalf("envelope = %q, want ok", body.Status)
	}

	// The payload is the providers projection, not some other handler's answer.
	var env struct {
		Data modelProviders `json:"data"`
	}
	if err := json.Unmarshal(msg.Root().Bytes(4), &env); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if want := listRouteProviders(); !reflect.DeepEqual(env.Data.Providers, want) {
		t.Errorf("providers = %v, want %v — a different handler answered", env.Data.Providers, want)
	}
	if len(env.Data.Providers) == 0 {
		t.Error("providers is empty — the 200 carries no catalogue")
	}
}

// TestGatewayDeclineKeepsOwnRoutes proves declining costs the access handler
// nothing: the paths it does serve still reach it. No credential is supplied, so
// a 401 from its own auth gate is proof the handler ran rather than stepped aside.
func TestGatewayDeclineKeepsOwnRoutes(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		msg, handled, err := dispatchGateway(context.Background(), nil, method, "/v1/models/enso/access", "", "", nil)
		if err != nil {
			t.Fatalf("%s: dispatch: %v", method, err)
		}
		if !handled {
			t.Fatalf("%s /v1/models/{model}/access not handled — the access route was lost", method)
		}
		if status, _ := gwResp(t, msg); status != 401 {
			t.Errorf("%s /v1/models/{model}/access status = %d, want 401 from its own gate", method, status)
		}
	}
}

// TestGatewayDeclineFallsThroughToRouter proves a decline propagates the whole
// way down. A path under the "/v1/models/" prefix that NO registry entry claims
// must reach the native router — the same fallback an unregistered path gets.
// Reporting it handled would be the original bug wearing a different mask.
func TestGatewayDeclineFallsThroughToRouter(t *testing.T) {
	const orphan = "/v1/models/enso/nope"

	if _, handled, err := dispatchGateway(context.Background(), nil, "GET", orphan, "", "", nil); err != nil || handled {
		t.Fatalf("no router supplied: handled=%v err=%v, want false/nil — the decline was swallowed", handled, err)
	}

	var served string
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusTeapot)
	})

	_, handled, err := dispatchGateway(context.Background(), router, "GET", orphan, "", "", nil)
	if err != nil || !handled {
		t.Fatalf("router supplied: handled=%v err=%v, want true/nil", handled, err)
	}
	if served != "GET "+orphan {
		t.Errorf("router saw %q, want %q — the declined path never reached it", served, "GET "+orphan)
	}
}
