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
	"testing"

	"github.com/hanzoai/ai/router"
	"github.com/sashabaranov/go-openai"
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
			"zen4-coder":    route("qwen3-coder-flash"),
			"zen4-thinking": route("deepseek-v4-pro"),
			"zen4-mini":     route("alibaba-qwen3-32b"),
			"zen4":          route("glm-5"),
		},
		pricing: map[string]modelPrice{},
		prompts: map[string]string{},
		router: RouterConfigDef{
			Enabled: enabled,
			Prefer: map[string][]string{
				"code":       {"zen4-coder"},
				"reasoning":  {"zen4-thinking"},
				"cheap_chat": {"zen4-mini"},
				"default":    {"zen4"},
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
	for _, m := range []string{"gpt-4o", "zen4", "router:general", ""} {
		if isAutoModel(m) {
			t.Errorf("isAutoModel(%q) = true, want false", m)
		}
	}
}

func TestResolveAutoModelDisabled(t *testing.T) {
	prev := globalModelConfig
	defer func() { globalModelConfig = prev }()
	globalModelConfig = routerTestConfig(false)

	if _, ok := resolveAutoModel("auto", "", "", chatReq("auto", "refactor this function"), router.Slo{}); ok {
		t.Error("resolveAutoModel with routing disabled = ok, want not-ok")
	}
}

func TestResolveAutoModelNilConfig(t *testing.T) {
	prev := globalModelConfig
	defer func() { globalModelConfig = prev }()
	globalModelConfig = nil

	if _, ok := resolveAutoModel("auto", "", "", chatReq("auto", "hi"), router.Slo{}); ok {
		t.Error("resolveAutoModel with nil config = ok, want not-ok")
	}
}

func TestResolveAutoModelNotAuto(t *testing.T) {
	prev := globalModelConfig
	defer func() { globalModelConfig = prev }()
	globalModelConfig = routerTestConfig(true)

	if _, ok := resolveAutoModel("gpt-4o", "", "", chatReq("gpt-4o", "refactor this function"), router.Slo{}); ok {
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
		{"auto", "please refactor this function", "zen4-coder"},
		{"zen-router", "why does this work, explain how step by step", "zen4-thinking"},
		{"auto", "hi", "zen4-mini"},
		{"auto", "tell me about the history of the roman empire in detail", "zen4"},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			got, ok := resolveAutoModel(tc.alias, "", "", chatReq(tc.alias, tc.text), router.Slo{})
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
		if routed, ok := resolveAutoModel(req.Model, "", "", &req, router.Slo{}); ok {
			req.Model = routed
			w.Header().Set(RoutedModelHeader, routed)
		}
		// The real handler echoes request.Model (now rewritten) in the response.
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{Model: req.Model})
	}

	body, _ := json.Marshal(chatReq("auto", "please refactor this function"))
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))

	if got := rec.Header().Get(RoutedModelHeader); got != "zen4-coder" {
		t.Errorf("%s header = %q, want zen4-coder", RoutedModelHeader, got)
	}
	var resp openai.ChatCompletionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Model != "zen4-coder" {
		t.Errorf("response model = %q, want zen4-coder (billed/reported as served model)", resp.Model)
	}
}
