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
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A family names the arm that answered, the vendor that ran it and the arms that
// failed first. The arm reaches the caller; the vendor and the failures are read
// into the record and go no further.
func TestTheFamilyServingIsReadAndOnlyTheArmIsRelayed(t *testing.T) {
	cooled.forget()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(servedHeader, "z-ai/glm-5.3")
		w.Header().Set(providerHeader, "digitalocean")
		w.Header().Set(failoverHeader, "deepseek-v4 (openrouter): upstream status 429")
		_, _ = w.Write([]byte(`{"id":"gen-1","model":"enso","choices":[{"index":0,"message":{"role":"assistant","content":"4"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":6,"completion_tokens_details":{"reasoning_tokens":4}}}`))
	}))
	defer srv.Close()

	fam := otherFamily(t, srv.URL)
	body := []byte(`{"model":"enso","messages":[{"role":"user","content":"2+2?"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if refused := c.pipeToFamily(fam, "chat/completions", "openai", "enso", body, false, 0, "acme", nil, false, nil, time.Now()); refused != nil {
		t.Fatalf("the family refused: %+v", refused)
	}
	h := &c.Fiber().Response().Header
	if got := string(h.Peek(servedHeader)); got != "z-ai/glm-5.3" {
		t.Errorf("%s = %q, want the arm", servedHeader, got)
	}
	for _, k := range []string{providerHeader, failoverHeader} {
		if got := string(h.Peek(k)); got != "" {
			t.Errorf("%s = %q reached the caller", k, got)
		}
	}

	hdr := http.Header{}
	hdr.Set(servedHeader, "z-ai/glm-5.3")
	hdr.Set(providerHeader, "digitalocean")
	hdr.Set(failoverHeader, "a (b): c")
	if got := servingOf(hdr); got.arm != "z-ai/glm-5.3" || got.vendor != "digitalocean" || got.failover != "a (b): c" {
		t.Errorf("servingOf = %+v", got)
	}
}

// A streamed answer's first token is the first data chunk the relay writes.
func TestTheStreamRelayMarksItsFirstChunk(t *testing.T) {
	to := toStream()
	before := time.Now()
	body := ": keepalive\n\n" +
		`data: {"id":"gen-1","choices":[{"delta":{"content":"4"}}]}` + "\n\n" +
		`data: {"id":"gen-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":6,"completion_tokens_details":{"reasoning_tokens":4}}}` + "\n\n" +
		"data: [DONE]\n\n"
	used, _, _, first := relayZenStream(to.w, strings.NewReader(body), nil)
	if first.IsZero() || first.Before(before) {
		t.Errorf("first = %v, want the moment the first data chunk was written", first)
	}
	if used.reasoning != 4 || used.completion != 6 {
		t.Errorf("tokens = %+v, want 4 reasoning of 6 completion", used)
	}
}

// Reasoning is a part of the completion, read from OpenAI's completion details.
func TestSniffZenUsageReadsReasoning(t *testing.T) {
	var got tokens
	sniffZenUsage([]byte(`{"usage":{"prompt_tokens":214,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":20},"prompt_tokens_details":{"cached_tokens":14}}}`), &got)
	if got != (tokens{fresh: 200, cached: 14, completion: 20, reasoning: 20}) {
		t.Errorf("tokens = %+v", got)
	}
	var none tokens
	sniffZenUsage([]byte(`{"usage":{"prompt_tokens":3,"completion_tokens":1}}`), &none)
	if none.reasoning != 0 {
		t.Errorf("reasoning = %d with no details", none.reasoning)
	}
}

// A served usage counts its prompt whole, with the cached part inside it; the
// record counts the fresh part and the cached part beside each other.
func TestServedRecordCountsThePromptAsTheFamilyPathDoes(t *testing.T) {
	rec := servedRecord(ServedUsage{
		Owner: "acme", Model: "zen5", Provider: "zen", Status: "success",
		PromptTokens: 214, CachedTokens: 14, CompletionTokens: 20, ReasoningTokens: 20,
		Served: "z-ai/glm-5.3", Vendor: "digitalocean", Failover: "a (b): c", First: 900 * time.Millisecond,
	})
	if rec.PromptTokens != 200 || rec.CacheReadTokens != 14 || rec.TotalTokens != 234 {
		t.Errorf("prompt=%d cached=%d total=%d, want 200/14/234", rec.PromptTokens, rec.CacheReadTokens, rec.TotalTokens)
	}
	if rec.Served != "z-ai/glm-5.3" || rec.Vendor != "digitalocean" || rec.Failover != "a (b): c" ||
		rec.ReasoningTokens != 20 || rec.First != 900*time.Millisecond {
		t.Errorf("served facts lost: %+v", rec)
	}
	// A cached count larger than the prompt is clamped to it, never negative fresh.
	if r := servedRecord(ServedUsage{PromptTokens: 5, CachedTokens: 9}); r.PromptTokens != 0 || r.CacheReadTokens != 5 {
		t.Errorf("clamp: prompt=%d cached=%d", r.PromptTokens, r.CacheReadTokens)
	}
}
