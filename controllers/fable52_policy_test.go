// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

func TestFable52AliasesAndPricing(t *testing.T) {
	wantAliases := map[string]string{
		"claude-fable-5.2":           "anthropic/claude-fable-5.2",
		"claude-fable-5-2":           "anthropic/claude-fable-5.2",
		"anthropic/claude-fable-5-2": "anthropic/claude-fable-5.2",
		"claude-fable-5.1":           "anthropic/claude-fable-5.1",
		"claude-opus-5.5":            "anthropic/claude-opus-5.5",
		"claude-sonnet-5.5":          "anthropic/claude-sonnet-5.5",
	}
	for k, want := range wantAliases {
		if got := openrouterAliases[k]; got != want {
			t.Errorf("openrouterAliases[%q] = %q, want %q", k, got, want)
		}
	}
	if got := CanonicalModel("claude-fable-5-2"); got != "anthropic/claude-fable-5.2" {
		t.Errorf("CanonicalModel(claude-fable-5-2) = %q, want anthropic/claude-fable-5.2", got)
	}
	if got := CanonicalModel("claude-fable-5.2"); got != "anthropic/claude-fable-5.2" {
		t.Errorf("CanonicalModel(claude-fable-5.2) = %q, want anthropic/claude-fable-5.2", got)
	}

	prevCfg := globalModelConfig
	globalModelConfig = nil
	t.Cleanup(func() { globalModelConfig = prevCfg })

	for _, id := range []string{"claude-fable-5.2", "claude-fable-5-2", "anthropic/claude-fable-5.2"} {
		p, ok := getModelPriceForOrgOK(id, "")
		if !ok {
			t.Fatalf("getModelPriceForOrgOK(%q) ok=false, want true", id)
		}
		if p.InputPerMillion != 8.00 || p.OutputPerMillion != 40.00 ||
			p.CacheReadPerMillion != 0.20 || p.CacheWritePerMillion != 10.00 ||
			p.CacheWrite1hPerMillion != 16.00 ||
			p.FastInputPerMillion != 16.00 || p.FastOutputPerMillion != 80.00 ||
			p.ContextWindow != 1_000_000 || p.MaxOutput != 128_000 {
			t.Errorf("pricing for %q = %+v, want Fable 5.2 rates ($8/$40, 0.20 read, 10 5m, 16 1h, 16/80 fast, 1M ctx, 128k out)", id, p)
		}
	}
	if got := mostOut("claude-fable-5.2"); got != 128_000 {
		t.Errorf("mostOut(claude-fable-5.2) = %d, want 128000", got)
	}
	if mins := hourAsMinutes("claude-fable-5.2", 1000); mins != 1600 {
		t.Errorf("hourAsMinutes(claude-fable-5.2, 1000) = %d, want 1600", mins)
	}
}

func TestFable52CapabilitiesAnd400Rules(t *testing.T) {
	caps := claudeCapabilities("claude-fable-5-2")
	if caps.RetentionDays != 30 || caps.ZeroRetention || !caps.Unforced || !caps.RejectSampling ||
		caps.ThinkingFamily != "fable-5.2" || !caps.Adaptive || !caps.Effort || !caps.MaxEffort || !caps.XHigh ||
		caps.Budget || caps.ContextWindow != 1_000_000 || caps.MaxOutput != 128_000 {
		t.Fatalf("claudeCapabilities(claude-fable-5-2) = %+v", caps)
	}

	// Direct chatAsMessages: rejects forced tool_choice and explicit sampling parameters rather than silently dropping.
	if _, _, err := chatAsMessages([]byte(`{"model":"claude-fable-5-2","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"tool_choice":"required"}`), caps); statusOf(err) != http.StatusBadRequest || codeOf(err) != codeInvalidRequestError {
		t.Errorf("chatAsMessages forced tool_choice = (%v, status=%d, code=%q), want 400 invalid_request_error", err, statusOf(err), codeOf(err))
	}
	for _, sampling := range []string{
		`{"model":"claude-fable-5-2","messages":[{"role":"user","content":"hi"}],"temperature":0}`,
		`{"model":"claude-fable-5-2","messages":[{"role":"user","content":"hi"}],"top_p":0.9}`,
		`{"model":"claude-fable-5-2","messages":[{"role":"user","content":"hi"}],"top_k":40}`,
	} {
		if _, _, err := chatAsMessages([]byte(sampling), caps); statusOf(err) != http.StatusBadRequest || codeOf(err) != codeInvalidRequestError {
			t.Errorf("chatAsMessages sampling %s = (%v, status=%d, code=%q), want 400 invalid_request_error", sampling, err, statusOf(err), codeOf(err))
		}
	}

	// End-to-end controller pipeToFamily: 400 invalid_request_error on both /v1/chat/completions and /v1/messages without hitting upstream.
	upstreamCalled := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fam := spareFamily(t, srv.URL, "anthropic/claude-fable-5.2", "vendor/free")
	for _, tc := range []struct {
		name    string
		apiPath string
		dialect string
		body    string
	}{
		{"chat forced tool_choice", "chat/completions", "openai", `{"model":"claude-fable-5-2","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"required"}`},
		{"chat named tool_choice", "chat/completions", "openai", `{"model":"claude-fable-5-2","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":{"type":"function","function":{"name":"f"}}}`},
		{"chat temperature", "chat/completions", "openai", `{"model":"claude-fable-5-2","messages":[{"role":"user","content":"hi"}],"temperature":0.5}`},
		{"chat top_p", "chat/completions", "openai", `{"model":"claude-fable-5-2","messages":[{"role":"user","content":"hi"}],"top_p":0.8}`},
		{"chat top_k", "chat/completions", "openai", `{"model":"claude-fable-5-2","messages":[{"role":"user","content":"hi"}],"top_k":10}`},
		{"messages forced any", "messages", "anthropic", `{"model":"claude-fable-5-2","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"any"}}`},
		{"messages forced tool", "messages", "anthropic", `{"model":"claude-fable-5-2","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"tool","name":"f"}}`},
		{"messages temperature", "messages", "anthropic", `{"model":"claude-fable-5-2","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"temperature":0}`},
		{"messages top_p", "messages", "anthropic", `{"model":"claude-fable-5-2","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"top_p":0.95}`},
		{"messages top_k", "messages", "anthropic", `{"model":"claude-fable-5-2","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"top_k":20}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := visit(http.MethodPost, "/v1/"+tc.apiPath)
			c.Fiber().Request().SetBody([]byte(tc.body))
			if out := c.pipeToFamily(fam, tc.apiPath, tc.dialect, "claude-fable-5-2", []byte(tc.body), false, 64, "acme", nil, false, nil, time.Now()); out != nil {
				t.Fatalf("unexpected attempts returned: %+v", out)
			}
			if answered(c) != http.StatusBadRequest || !strings.Contains(sent(c), codeInvalidRequestError) {
				t.Errorf("%s: status=%d body=%s, want 400 invalid_request_error", tc.name, answered(c), sent(c))
			}
		})
	}
	if upstreamCalled != 0 {
		t.Errorf("upstream called %d times on invalid Fable 5.2 requests, want 0", upstreamCalled)
	}
}

func TestFable52ZDR403AndOpenRouterZDR(t *testing.T) {
	var seenHeaders []http.Header
	var seenBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = append(seenHeaders, r.Header.Clone())
		b, _ := io.ReadAll(r.Body)
		seenBodies = append(seenBodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-1","model":"anthropic/claude-fable-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	fam := spareFamily(t, srv.URL, "anthropic/claude-fable-5.2", "anthropic/claude-sonnet-5.5")
	fam.terms = openrouterTerms

	// Non-BYO ZDR to Fable 5.2 -> 403 retention_incompatible (both 1 and true, both dialects).
	for _, zdrVal := range []string{"1", "true"} {
		for _, pathDialect := range [][2]string{{"messages", "anthropic"}, {"chat/completions", "openai"}} {
			c := visit(http.MethodPost, "/v1/"+pathDialect[0])
			c.Fiber().Request().Header.Set(zdrHeader, zdrVal)
			body := []byte(`{"model":"claude-fable-5-2","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
			c.Fiber().Request().SetBody(body)
			c.pipeToFamily(fam, pathDialect[0], pathDialect[1], "claude-fable-5-2", body, false, 64, "acme", nil, false, nil, time.Now())
			if answered(c) != http.StatusForbidden || !strings.Contains(sent(c), codeRetentionIncompatible) {
				t.Errorf("ZDR=%q %s: status=%d body=%s, want 403 retention_incompatible", zdrVal, pathDialect[0], answered(c), sent(c))
			}
		}
	}
	if len(seenBodies) != 0 {
		t.Fatalf("non-BYO ZDR reached upstream %d times", len(seenBodies))
	}

	// OpenRouter + ZDR on a ZDR-compatible SKU: sets provider.zdr=true in body AND forwards X-Hanzo-ZDR: 1.
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().Header.Set(zdrHeader, "true")
	body := []byte(`{"model":"anthropic/claude-sonnet-5.5","messages":[{"role":"user","content":"hi"}]}`)
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(fam, "chat/completions", "openai", "anthropic/claude-sonnet-5.5", body, false, 64, "acme", nil, false, nil, time.Now()); out != nil {
		t.Fatalf("ZDR compatible failed: %+v", out)
	}
	if answered(c) != http.StatusOK || len(seenBodies) != 1 || len(seenHeaders) != 1 {
		t.Fatalf("status=%d seenBodies=%d", answered(c), len(seenBodies))
	}
	if got := seenHeaders[0].Get(zdrHeader); got != "1" {
		t.Errorf("forwarded %s = %q, want 1", zdrHeader, got)
	}
	var forwarded struct {
		Provider struct {
			ZDR bool `json:"zdr"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(seenBodies[0]), &forwarded); err != nil || !forwarded.Provider.ZDR {
		t.Errorf("OpenRouter forwarded body %s missing provider.zdr=true (err=%v)", seenBodies[0], err)
	}

	// BYO with X-Hanzo-ZDR-Authorized: 1 allows non-ZDR Fable 5.2, while BYO without it is still refused 403.
	if err := validateClaudeRequest("claude-fable-5-2", "messages", body, claudeCapabilities("claude-fable-5-2"), true, true, false); statusOf(err) != http.StatusForbidden || codeOf(err) != codeRetentionIncompatible {
		t.Errorf("BYO without authorization = (%v, %d, %q), want 403 retention_incompatible", err, statusOf(err), codeOf(err))
	}
	if err := validateClaudeRequest("claude-fable-5-2", "messages", body, claudeCapabilities("claude-fable-5-2"), true, true, true); err != nil {
		t.Errorf("BYO with authorization rejected: %v", err)
	}
}

func TestNativeRequestStripsThinkingOnFamilySwitch(t *testing.T) {
	prov := &object.Provider{ProviderUrl: "https://api.anthropic.example", ClientSecret: "sk-ant-test"}
	withPriorThinking := `{"model":"claude-fable-5-2","max_tokens":1024,"messages":[{"role":"user","content":"step 1"},{"role":"assistant","content":[{"type":"thinking","thinking":"old reasoning","signature":"sig-opus"},{"type":"redacted_thinking","data":"cipher-opus"},{"type":"text","text":"kept prose"},{"type":"tool_use","id":"call_1","name":"search","input":{"q":"x"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}]}`
	set := map[string]any{"model": "claude-fable-5-2", "max_tokens": 1024}

	// Switching ThinkingFamily via X-Hanzo-Prior-Model strips prior assistant thinking/redacted_thinking while keeping text & tool_use.
	ctxSwitched := WithPriorModel(context.Background(), "claude-opus-4-6")
	req, err := nativeRequest(ctxSwitched, prov, []byte(withPriorThinking), set, true, "", nil)
	if err != nil {
		t.Fatalf("nativeRequest switched failed: %v", err)
	}
	outBytes, _ := io.ReadAll(req.Body)
	outStr := string(outBytes)
	for _, banned := range []string{`"thinking"`, `"redacted_thinking"`, "old reasoning", "cipher-opus", "sig-opus"} {
		if strings.Contains(outStr, banned) {
			t.Errorf("switched ThinkingFamily kept %s in body: %s", banned, outStr)
		}
	}
	for _, kept := range []string{"kept prose", `"type":"tool_use"`, `"id":"call_1"`, `"type":"tool_result"`} {
		if !strings.Contains(outStr, kept) {
			t.Errorf("switched ThinkingFamily lost %s in body: %s", kept, outStr)
		}
	}

	// Same ThinkingFamily keeps signature & redacted_thinking intact byte-for-byte.
	ctxSame := WithPriorModel(context.Background(), "claude-fable-5-2-20261101")
	reqSame, err := nativeRequest(ctxSame, prov, []byte(withPriorThinking), set, true, "", nil)
	if err != nil {
		t.Fatalf("nativeRequest same family failed: %v", err)
	}
	sameBytes, _ := io.ReadAll(reqSame.Body)
	sameStr := string(sameBytes)
	if !strings.Contains(sameStr, "sig-opus") || !strings.Contains(sameStr, `"redacted_thinking"`) {
		t.Errorf("same ThinkingFamily stripped thinking blocks: %s", sameStr)
	}
}

func TestGenAITraceAttributesRuntimeDigestAndCacheConnector(t *testing.T) {
	rec := servedRecord(ServedUsage{
		Owner:            "acme",
		Model:            "claude-fable-5.2",
		Provider:         "enso",
		Served:           "anthropic/claude-fable-5.2",
		Vendor:           "anthropic",
		RuntimeDigest:    "sha256:rt-abc123",
		CacheConnector:   "kv-fabric-west",
		PromptTokens:     100,
		CompletionTokens: 20,
	})
	m := attrMap(buildGenAISpanFields(rec, 0.01, 0.01, nil, nil, nil, false).attrs)
	if got := m[attrRuntimeDigest].AsString(); got != "sha256:rt-abc123" {
		t.Errorf("%s = %q, want sha256:rt-abc123", attrRuntimeDigest, got)
	}
	if got := m[attrCacheConnector].AsString(); got != "kv-fabric-west" {
		t.Errorf("%s = %q, want kv-fabric-west", attrCacheConnector, got)
	}

	// Also verify servingOf captures X-Hanzo-Runtime-Digest and X-Hanzo-Cache-Connector.
	h := http.Header{
		runtimeDigestHeader:  {"sha256:hdr-999"},
		cacheConnectorHeader: {"kv-tcp-02"},
	}
	sv := servingOf(h)
	if sv.runtimeDigest != "sha256:hdr-999" || sv.cacheConnector != "kv-tcp-02" {
		t.Errorf("servingOf = %+v, want sha256:hdr-999 / kv-tcp-02", sv)
	}
}

func TestBudgetExhaustedUpstreamProvider503(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"exhausted with 402 attempt", exhausted("claude-fable-5.2", []attempt{{provider: "openrouter", status: http.StatusPaymentRequired, err: errors.New("Insufficient credits")}})},
		{"direct upstream 402", &apiError{status: http.StatusPaymentRequired, msg: "upstream out of credit"}},
	} {
		c := visit(http.MethodPost, "/v1/chat/completions")
		c.ResponseModelFailure(tc.err)
		if answered(c) != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", tc.name, answered(c))
		}
		var body Response
		if err := json.Unmarshal([]byte(sent(c)), &body); err != nil {
			t.Fatalf("%s: unmarshal %s: %v", tc.name, sent(c), err)
		}
		if body.Code != codeBudgetExhausted || body.Layer != layerUpstreamProvider {
			t.Errorf("%s: Response = %+v, want code=%q layer=%q", tc.name, body, codeBudgetExhausted, layerUpstreamProvider)
		}
	}
}
