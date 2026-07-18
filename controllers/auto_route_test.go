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

package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
	"github.com/hanzoai/go-openai"
	iam "github.com/hanzoai/iam"
)

// routerTestConfig builds a ModelConfig with auto-routing enabled and a small
// prefer table whose ids are all present as routes (so the servability predicate
// resolveModelRouteForOrg admits them). The router is heuristic-only (no engine
// endpoint), so classification is deterministic.
func routerTestConfig(enabled bool) *ModelConfig {
	route := func(name string) modelRoute {
		return modelRoute{providerName: "do-ai", upstreamModel: name}
	}
	return &ModelConfig{
		routes: map[string]modelRoute{
			"glm-5.2":           route("qwen3-coder-flash"),
			"deepseek-v4-pro":   route("deepseek-v4-pro"),
			"deepseek-v4-flash": route("alibaba-qwen3-32b"),
			"gpt-4o":            route("glm-5"),
		},
		pricing: map[string]modelPrice{},
		router: RouterConfigDef{
			Enabled: enabled,
			Prefer: map[string][]string{
				"code":       {"glm-5.2"},
				"reasoning":  {"deepseek-v4-pro"},
				"cheap_chat": {"deepseek-v4-flash"},
				"default":    {"gpt-4o"},
			},
		},
	}
}

func chatReq(model, userText string) *openai.ChatCompletionRequest {
	return &openai.ChatCompletionRequest{
		Model:    model,
		Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: userText}},
	}
}

func TestIsAutoModel(t *testing.T) {
	for _, m := range []string{"auto", "AUTO", "zen-router", " auto "} {
		if !isAutoModel(m) {
			t.Errorf("isAutoModel(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"gpt-4o", "gpt-4o", "router:general", ""} {
		if isAutoModel(m) {
			t.Errorf("isAutoModel(%q) = true, want false", m)
		}
	}
}

func TestResolveAutoModelDisabled(t *testing.T) {
	prev := globalModelConfig
	defer func() { globalModelConfig = prev }()
	globalModelConfig = routerTestConfig(false)

	if _, _, ok := resolveAutoModel("auto", "", "", "", nil, chatReq("auto", "refactor this function"), router.Slo{}); ok {
		t.Error("resolveAutoModel with routing disabled = ok, want not-ok")
	}
}

func TestResolveAutoModelNilConfig(t *testing.T) {
	prev := globalModelConfig
	defer func() { globalModelConfig = prev }()
	globalModelConfig = nil

	if _, _, ok := resolveAutoModel("auto", "", "", "", nil, chatReq("auto", "hi"), router.Slo{}); ok {
		t.Error("resolveAutoModel with nil config = ok, want not-ok")
	}
}

func TestResolveAutoModelNotAuto(t *testing.T) {
	prev := globalModelConfig
	defer func() { globalModelConfig = prev }()
	globalModelConfig = routerTestConfig(true)

	if _, _, ok := resolveAutoModel("gpt-4o", "", "", "", nil, chatReq("gpt-4o", "refactor this function"), router.Slo{}); ok {
		t.Error("resolveAutoModel for a concrete model = ok, want not-ok")
	}
}

func TestResolveAutoModelEnabled(t *testing.T) {
	prev := globalModelConfig
	defer func() { globalModelConfig = prev }()
	globalModelConfig = routerTestConfig(true)

	cases := []struct {
		alias string
		text  string
		want  string
	}{
		{"auto", "please refactor this function", "glm-5.2"},
		{"zen-router", "why does this work, explain how step by step", "deepseek-v4-pro"},
		{"auto", "hi", "deepseek-v4-flash"},
		{"auto", "tell me about the history of the roman empire in detail", "gpt-4o"},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			got, _, ok := resolveAutoModel(tc.alias, "", "", "", nil, chatReq(tc.alias, tc.text), router.Slo{})
			if !ok {
				t.Fatalf("resolveAutoModel(%q) not ok", tc.text)
			}
			if got != tc.want {
				t.Errorf("resolveAutoModel(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestAutoRoutingHTTPContract proves the end-to-end HTTP contract the chat
// controller implements: an `auto` request is rewritten to the resolved model,
// the X-Routed-Model header carries it, and the response body echoes the SAME id
// in `model` (so billing/usage — which key off request.Model — bind to the
// served model). This exercises the real resolveAutoModel + header name without
// the full auth/billing stack.
func TestAutoRoutingHTTPContract(t *testing.T) {
	prev := globalModelConfig
	defer func() { globalModelConfig = prev }()
	globalModelConfig = routerTestConfig(true)

	handler := func(w http.ResponseWriter, r *http.Request) {
		var req openai.ChatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if routed, _, ok := resolveAutoModel(req.Model, "", "", "", nil, &req, router.Slo{}); ok {
			req.Model = routed
			w.Header().Set(RoutedModelHeader, routed)
		}
		// The real handler echoes request.Model (now rewritten) in the response.
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{Model: req.Model})
	}

	body, _ := json.Marshal(chatReq("auto", "please refactor this function"))
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))

	if got := rec.Header().Get(RoutedModelHeader); got != "glm-5.2" {
		t.Errorf("%s header = %q, want glm-5.2", RoutedModelHeader, got)
	}
	var resp openai.ChatCompletionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Model != "glm-5.2" {
		t.Errorf("response model = %q, want glm-5.2 (billed/reported as served model)", resp.Model)
	}
}

// TestResolveAutoModelGatedServabilityGate proves the HIP-510 §1 invariant: the
// `known` predicate admits an access-gated SKU ONLY when the caller holds a grant, so
// `auto` never routes to a gated model the serve path would then refuse (the auto→enso
// 404). enso-flash here is a gated (waitlist) family SKU whose route resolves; glm-5.2 is
// a plain servable fallback; code prompts prefer enso-flash then glm-5.2. Without a grant
// the router skips enso-flash and falls through to glm-5.2; with a grant it is chosen. The
// gated status is real (discovered catalog, free tier so the tier gate is a no-op); only
// the grant lookup — the DB leaf — is seamed, exercising the true grantAllows composition.
func TestResolveAutoModelGatedServabilityGate(t *testing.T) {
	prevCfg, prevSink, prevGrant, savedCatalog := globalModelConfig, routingEventSink, modelAccessGrantedFn, ensoFam.byID
	t.Cleanup(func() {
		globalModelConfig, routingEventSink, modelAccessGrantedFn, ensoFam.byID = prevCfg, prevSink, prevGrant, savedCatalog
	})

	globalModelConfig = &ModelConfig{
		routes: map[string]modelRoute{
			"enso-flash": {providerName: "enso", upstreamModel: "enso-flash"},
			"glm-5.2":    {providerName: "do-ai", upstreamModel: "glm-5.2"},
		},
		pricing: map[string]modelPrice{},
		router: RouterConfigDef{
			Enabled: true,
			Prefer:  map[string][]string{"code": {"enso-flash", "glm-5.2"}},
		},
	}
	// Gated (waitlist) but MinTier "" ⇒ free, so the tier gate passes with no commerce
	// call and the grant gate is the sole decider.
	ensoFam.byID = map[string]zenModel{"enso-flash": {ID: "enso-flash", Access: "waitlist"}}

	var events []object.RoutingEvent
	routingEventSink = func(e object.RoutingEvent) { events = append(events, e) }

	user := &iam.User{Owner: "acme", Name: "alice", Email: "alice@acme.co"}
	const codePrompt = "please refactor this function"

	// (a) No grant → the gated SKU is NOT admitted by `known`; auto falls through to
	// the next servable candidate.
	modelAccessGrantedFn = func(owner, name, email, model string) bool { return false }
	got, _, ok := resolveAutoModel("auto", "acme", "acme/alice", "r1", user, chatReq("auto", codePrompt), router.Slo{})
	if !ok {
		t.Fatal("resolveAutoModel not ok (no grant)")
	}
	if got != "glm-5.2" {
		t.Fatalf("without grant: routed to %q, want glm-5.2 (gated enso-flash must be skipped)", got)
	}

	// (b) With a grant for enso-flash → the gated SKU IS admitted and chosen.
	modelAccessGrantedFn = func(owner, name, email, model string) bool {
		return owner == "acme" && model == "enso-flash"
	}
	got, _, ok = resolveAutoModel("auto", "acme", "acme/alice", "r2", user, chatReq("auto", codePrompt), router.Slo{})
	if !ok {
		t.Fatal("resolveAutoModel not ok (granted)")
	}
	if got != "enso-flash" {
		t.Fatalf("with grant: routed to %q, want enso-flash", got)
	}

	// The content-free RoutingEvent recording stays intact: one event per resolve, the
	// routed model recorded, and never any prompt text.
	if len(events) != 2 {
		t.Fatalf("recorded %d routing events, want 2", len(events))
	}
	if events[1].RoutedModel != "enso-flash" || events[1].RequestedModel != "auto" {
		t.Errorf("granted event routed/requested = %q/%q, want enso-flash/auto", events[1].RoutedModel, events[1].RequestedModel)
	}
	for _, e := range events {
		blob := strings.ToLower(e.Task + e.RoutedModel + e.RequestedModel + e.Features + e.User + e.Owner + e.RequestId)
		if strings.Contains(blob, "refactor") {
			t.Errorf("routing event leaked prompt text: %+v", e)
		}
	}
}

// TestRecordExplicitRoutingCapturesSelection proves a non-auto request is recorded
// as a rateable data point: source=explicit, requested==routed==the model, keyed
// by request id so an up/down reward can attach, task classified from the prompt.
func TestRecordExplicitRoutingCapturesSelection(t *testing.T) {
	var got object.RoutingEvent
	prev := routingEventSink
	routingEventSink = func(e object.RoutingEvent) { got = e }
	defer func() { routingEventSink = prev }()

	task := recordExplicitRouting("GPT-4o", "acme", "acme/z", "req-123",
		chatReq("gpt-4o", "Refactor this function and fix the bug"))
	if task != "code" {
		t.Errorf("task classify: got %q want code", task)
	}
	if got.Source != SourceExplicit || got.RequestId != "req-123" {
		t.Errorf("explicit event: source=%q reqid=%q", got.Source, got.RequestId)
	}
	if got.RequestedModel != "gpt-4o" || got.RoutedModel != "gpt-4o" {
		t.Errorf("model should be the caller's pick (lowered): req=%q routed=%q", got.RequestedModel, got.RoutedModel)
	}
	if got.Owner != "acme" {
		t.Errorf("owner: got %q", got.Owner)
	}
}
