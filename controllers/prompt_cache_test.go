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
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	openai "github.com/hanzoai/go-openai"
)

// cacheParts is usage.input_tokens_details as Codex reads it.
type cacheParts struct {
	Cached  int `json:"cached_tokens"`
	Written int `json:"cache_write_tokens"`
}

// inputDetails reads usage.input_tokens_details off a Responses object.
func inputDetails(t *testing.T, resource []byte) cacheParts {
	t.Helper()
	var r struct {
		Usage struct {
			InputDetails cacheParts `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(resource, &r); err != nil {
		t.Fatalf("not a Responses object: %v\n%s", err, resource)
	}
	return r.Usage.InputDetails
}

// completed is the response object of a stream's response.completed event.
func completed(t *testing.T, wire string) []byte {
	t.Helper()
	for _, frame := range strings.Split(wire, "\n\n") {
		if !strings.Contains(frame, "event: response.completed") {
			continue
		}
		_, data, _ := strings.Cut(frame, "data: ")
		var e struct {
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatalf("response.completed is not JSON: %v\n%s", err, frame)
		}
		return e.Response
	}
	t.Fatalf("no response.completed:\n%s", wire)
	return nil
}

// A Responses answer says how much of the prompt the upstream's cache served and
// how much it wrote, as the chat usage it translates said. It said 0 for both on
// every answer, so an agent resending its conversation each turn read every turn
// as a full-price prompt, whatever the cache did.
func TestAResponsesAnswerSaysWhatTheCacheServed(t *testing.T) {
	const usage = `"usage":{"prompt_tokens":1000,"completion_tokens":5,"total_tokens":1005,` +
		`"prompt_tokens_details":{"cached_tokens":900,"cache_write_tokens":60}}`
	want := cacheParts{Cached: 900, Written: 60}

	t.Run("streamed", func(t *testing.T) {
		out := &bytes.Buffer{}
		bridge := newResponsesBridge(out, &OpenAIResponsesRequest{Model: "m", Stream: true}, nil)
		upstream := strings.Join([]string{
			`data: {"id":"c","choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`,
			`data: {"id":"c","choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: {"id":"c","choices":[],` + usage + `}`,
			`data: [DONE]`, "",
		}, "\n\n")
		if _, err := bridge.Write([]byte(upstream)); err != nil {
			t.Fatal(err)
		}
		if err := bridge.Close(); err != nil {
			t.Fatal(err)
		}
		if got := inputDetails(t, completed(t, out.String())); got != want {
			t.Errorf("input_tokens_details = %+v, want %+v", got, want)
		}
	})

	t.Run("whole", func(t *testing.T) {
		chat := `{"id":"c","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` + usage + `}`
		body, err := openAIChatResponseToResponses([]byte(chat), &OpenAIResponsesRequest{Model: "m"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := inputDetails(t, body); got != want {
			t.Errorf("input_tokens_details = %+v, want %+v", got, want)
		}
	})

	// The cache's parts are parts of the prompt: a report claiming more than the
	// prompt held is read as all of it, never as more.
	t.Run("bounded by the prompt", func(t *testing.T) {
		chat := `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":1,` +
			`"prompt_tokens_details":{"cached_tokens":250,"cache_write_tokens":40}}}`
		body, err := openAIChatResponseToResponses([]byte(chat), &OpenAIResponsesRequest{Model: "m"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := inputDetails(t, body); got != (cacheParts{Cached: 100}) {
			t.Errorf("input_tokens_details = %+v, want all 100 cached and none written", got)
		}
	})
}

// The key a Responses caller names its conversation by reaches the chat body, where
// a vendor routes each turn to the cache holding its prefix. Codex sends one on
// every turn; it was dropped.
func TestAResponsesRequestKeepsItsPromptCacheKey(t *testing.T) {
	call, err := ReadResponses([]byte(`{"model":"m","input":"hi","prompt_cache_key":"thread-1"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(decoded(t, call.Chat)["prompt_cache_key"]); got != `"thread-1"` {
		t.Errorf("prompt_cache_key = %s, want \"thread-1\": %s", got, call.Chat)
	}
}

// A family is sent the cache fields with the rest of the request.
func TestAFamilyIsSentTheCacheFields(t *testing.T) {
	chat := decoded(t, familyBody([]byte(`{"model":"m","messages":[],"prompt_cache_key":"thread-1",`+
		`"cache_control":{"type":"ephemeral","ttl":"1h"}}`), "chat/completions", 0))
	if string(chat["prompt_cache_key"]) != `"thread-1"` || string(chat["cache_control"]) != `{"type":"ephemeral","ttl":"1h"}` {
		t.Errorf("chat family body lost a cache field: %v", chat)
	}
	msgs := decoded(t, familyBody([]byte(`{"model":"m","messages":[],"cache_control":{"type":"ephemeral"}}`), "messages", 0))
	if string(msgs["cache_control"]) != `{"type":"ephemeral"}` {
		t.Errorf("messages family body lost cache_control: %v", msgs)
	}
}

// OpenRouter is asked to cache a Claude prompt, which Anthropic caches only when the
// request marks it. A caller's own breakpoints are left as they are, and a model
// that caches on its own is sent nothing new.
func TestOpenRouterIsAskedToCacheAClaudePrompt(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
	}{
		{"claude", `{"model":"anthropic/claude-sonnet-5.5","messages":[{"role":"user","content":"hi"}]}`, `{"type":"ephemeral"}`},
		{"its own breakpoint", `{"model":"anthropic/claude-sonnet-5.5","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`, ``},
		{"its own top-level ttl", `{"model":"anthropic/claude-sonnet-5.5","cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[]}`, `{"type":"ephemeral","ttl":"1h"}`},
		{"not claude", `{"model":"openai/gpt-6","messages":[{"role":"user","content":"hi"}]}`, ``},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := string(decoded(t, openrouterTerms([]byte(c.body), false))["cache_control"])
			if got != c.want {
				t.Errorf("cache_control = %q, want %q", got, c.want)
			}
		})
	}
}

// A Claude row served directly is asked to cache the prompt too.
func TestTheAnthropicToolProxyAsksForTheCache(t *testing.T) {
	var sent map[string]json.RawMessage
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &sent)
		_, _ = w.Write([]byte(`{"id":"msg_up","content":[{"type":"text","text":"ok"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":3,"cache_read_input_tokens":900,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	c := visit(http.MethodPost, "/v1/chat/completions")
	request := openai.ChatCompletionRequest{Model: "claude-x"}
	provider := &object.Provider{Owner: "admin", Name: "anthropic", Type: "Anthropic", SubType: "claude-x", ProviderUrl: upstream.URL}
	c.proxyToolRequestAnthropic(provider, &request, "claude-x", time.Now(), nil, false, "", "req1", nil)

	if got := string(sent["cache_control"]); got != `{"type":"ephemeral"}` {
		t.Errorf("Anthropic was sent cache_control %q, want {\"type\":\"ephemeral\"}", got)
	}
	var answer struct {
		Usage struct {
			PromptDetails struct {
				Cached int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(c.Fiber().Response().Body(), &answer); err != nil || answer.Usage.PromptDetails.Cached != 900 {
		t.Errorf("cached_tokens = %d (err %v), want 900: %s", answer.Usage.PromptDetails.Cached, err, c.Fiber().Response().Body())
	}
}
