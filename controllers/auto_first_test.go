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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/klauspost/compress/zstd"
)

// routedWorld is routing on, a recorder for routing events, and a signed caller.
func routedWorld(t *testing.T) (token string, events *[]object.RoutingEvent) {
	t.Helper()
	prevCfg, prevSink, prevLookup := globalModelConfig, routingEventSink, orgAutoRoutingLookup
	globalModelConfig = routerTestConfig(true)
	var got []object.RoutingEvent
	routingEventSink = func(e object.RoutingEvent) { got = append(got, e) }
	orgAutoRoutingLookup = func(string) string { return object.AutoRoutingUnset }
	t.Cleanup(func() { globalModelConfig, routingEventSink, orgAutoRoutingLookup = prevCfg, prevSink, prevLookup })
	return mintUsageJWT(t, "acme", "ann"), &got
}

func modelOf(t *testing.T, body []byte) string {
	t.Helper()
	var b struct{ Model string }
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("body is not JSON: %q", body)
	}
	return b.Model
}

// `auto` is resolved to the SKU that serves it before anything prices the request:
// the request names that SKU, says so in X-Routed-Model, and carries the decision the
// handler keeps. A caller who does not authenticate is not routed.
func TestAutoIsResolvedToItsSKUBeforeTheGate(t *testing.T) {
	token, events := routedWorld(t)
	for _, id := range []string{"auto", "zen-router"} {
		*events = nil
		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().Header.Set("Authorization", "Bearer "+token)
		c.Fiber().Request().SetBody([]byte(`{"model":"` + id + `","stream":true,"messages":[{"role":"user","content":"hello there"}]}`))
		c.RouteAuto()
		got := modelOf(t, c.Body())
		if got == "" || IsAutoModel(got) || resolveModelRoute(got) == nil {
			t.Fatalf("%s: the request names %q, want a routed, servable SKU", id, got)
		}
		if h := string(c.Fiber().Response().Header.Peek(RoutedModelHeader)); h != got {
			t.Errorf("%s: %s = %q, request names %q", id, RoutedModelHeader, h, got)
		}
		r, ok := c.Locals(autoRoutedKey).(autoRouted)
		if !ok || r.routed != got || r.requestId == "" {
			t.Errorf("%s: decision left for the handler = %+v", id, r)
		}
		var b struct{ Stream bool }
		_ = json.Unmarshal(c.Body(), &b)
		if !b.Stream {
			t.Errorf("%s: the rewrite lost stream: %s", id, c.Body())
		}
		if len(*events) != 1 || (*events)[0].RoutedModel != got || (*events)[0].RequestId != r.requestId || (*events)[0].RequestedModel != id {
			t.Errorf("%s: routing events %+v", id, *events)
		}
	}

	*events = nil
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody([]byte(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
	c.RouteAuto()
	if got := modelOf(t, c.Body()); got != "auto" || len(*events) != 0 {
		t.Errorf("an unauthenticated request was routed to %q (%d events)", got, len(*events))
	}

	c = visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().Header.Set("Authorization", "Bearer "+token)
	c.Fiber().Request().SetBody([]byte(`{"model":"gpt-4o","messages":[]}`))
	c.RouteAuto()
	if len(*events) != 0 || c.Locals(autoRoutedKey) != nil {
		t.Error("a concrete model was routed")
	}
}

// A /v1/responses body, zstd or plain, is read in its own dialect and rewritten in
// it; a zstd body is expanded once so the gate reads the model it names.
func TestAResponsesRequestIsRoutedInItsOwnDialect(t *testing.T) {
	token, _ := routedWorld(t)
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, model string
		zstd        bool
		want        string // "routed": whatever SKU the router picks
	}{
		{"auto, zstd", "auto", true, "routed"},
		{"auto, plain", "auto", false, "routed"},
	} {
		body := []byte(`{"model":"` + tc.model + `","input":"hello there","stream":false}`)
		c := visit(http.MethodPost, "/v1/responses")
		c.Fiber().Request().Header.Set("Authorization", "Bearer "+token)
		if tc.zstd {
			c.Fiber().Request().Header.Set("Content-Encoding", "zstd")
			body = enc.EncodeAll(body, nil)
		}
		c.Fiber().Request().SetBody(body)
		c.RouteAuto()
		if got := modelOf(t, c.Body()); IsAutoModel(got) || resolveModelRoute(got) == nil {
			t.Errorf("%s: model %q, want a routed, servable SKU", tc.name, got)
		}
		if ce := string(c.Fiber().Request().Header.Peek("Content-Encoding")); ce != "" {
			t.Errorf("%s: Content-Encoding %q left on a plain body", tc.name, ce)
		}
		var b struct{ Input string }
		_ = json.Unmarshal(c.Body(), &b)
		if b.Input != "hello there" {
			t.Errorf("%s: the Responses body lost its input: %s", tc.name, c.Body())
		}
	}
}

// On a pod that has just started, a family SKU is priced from discovery on the first
// read: enso-auto costs nothing and enso-flash costs what the family says, rather
// than the unknown-model default that refused a free org plan_required.
func TestAFamilySKUIsPricedFromDiscoveryOnAColdPod(t *testing.T) {
	var asked int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		asked++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[`+
			`{"id":"enso-auto","owned_by":"hanzo","context_window":1000000,"pricing":{"input":"0","output":"0"}},`+
			`{"id":"enso-flash","owned_by":"hanzo","context_window":1000000,"funding":"prepaid","pricing":{"input":"0.42","output":"1.5"}}]}`)
	}))
	defer srv.Close()

	restore(t, ensoFam)
	t.Cleanup(func() { ensoFam.mu.Lock(); ensoFam.tried = time.Time{}; ensoFam.mu.Unlock() })
	ensoFam.providerFn = func() *object.Provider {
		return &object.Provider{Owner: "admin", Name: "enso", Category: "Model", Type: "Enso", State: "Active", ProviderUrl: srv.URL}
	}
	ensoFam.mu.Lock()
	ensoFam.byID, ensoFam.ids, ensoFam.loaded, ensoFam.fetchedAt, ensoFam.tried = nil, nil, false, time.Time{}, time.Time{}
	ensoFam.mu.Unlock()

	if !ModelCostsNothing("enso-auto", "acme") {
		t.Fatal("enso-auto on a cold pod is priced: a free org would be refused plan_required")
	}
	if ModelCostsNothing("enso-flash", "acme") {
		t.Error("enso-flash on a cold pod costs nothing")
	}
	if p, ok := getModelPriceForOrgOK("enso-flash", "acme"); !ok || p.InputPerMillion != 0.42 || p.OutputPerMillion != 1.5 {
		t.Errorf("enso-flash price %+v (ok=%v), want the discovered 0.42/1.50", p, ok)
	}
	if asked != 1 {
		t.Errorf("discovery was asked %d times, want once", asked)
	}

	// A family that cannot be reached is asked again only after warmRetry.
	srv.Close()
	ensoFam.mu.Lock()
	ensoFam.byID, ensoFam.ids, ensoFam.loaded, ensoFam.tried = nil, nil, false, time.Time{}
	ensoFam.mu.Unlock()
	start := time.Now()
	_ = ModelCostsNothing("enso-auto", "acme")
	_ = ModelCostsNothing("enso-auto", "acme")
	if time.Since(start) > 5*time.Second {
		t.Error("an unreachable family was asked on every read")
	}
}
