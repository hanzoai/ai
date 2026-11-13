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

package crossmodel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// These tests need no network and no credential: they run the checks against canned
// responses, both conforming and the divergences #138 is hunting. They are what keep
// the suite honest in a gate that cannot reach the gateway.

const openAIAnswer = `{"id":"chatcmpl-1","object":"chat.completion","created":1700000000,"model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"Blue is a primary color."},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":6,"total_tokens":15}}`

const openAIToolAnswer = `{"id":"chatcmpl-2","object":"chat.completion","created":1700000000,"model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Osaka\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":21,"completion_tokens":18,"total_tokens":39}}`

const anthropicAnswer = `{"id":"msg_1","type":"message","role":"assistant","model":"anthropic/claude-haiku-4.5","content":[{"type":"text","text":"Blue."}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":3}}`

// gatewayEnvelope is what api.hanzo.ai answers today for a 401 on either shape; it is
// the divergence the envelope checks are written to catch.
const gatewayEnvelope = `{"status":"error","msg":"authentication required: send Authorization: Bearer <API key>","data":null,"data2":null}`

func openAIChunks(chunks ...string) []Frame {
	frames := make([]Frame, 0, len(chunks)+1)
	for _, c := range chunks {
		frames = append(frames, Frame{Data: c})
	}
	return append(frames, Frame{Data: "[DONE]"})
}

const chunkRole = `{"id":"chatcmpl-3","object":"chat.completion.chunk","created":1700000000,"model":"x","choices":[{"index":0,"delta":{"role":"assistant"}}]}`
const chunkTextA = `{"id":"chatcmpl-3","object":"chat.completion.chunk","created":1700000000,"model":"x","choices":[{"index":0,"delta":{"content":"Blue "}}]}`
const chunkTextB = `{"id":"chatcmpl-3","object":"chat.completion.chunk","created":1700000000,"model":"x","choices":[{"index":0,"delta":{"content":"is a primary color."}}]}`
const chunkStop = `{"id":"chatcmpl-3","object":"chat.completion.chunk","created":1700000000,"model":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

func TestCheckOpenAIChatCompletion(t *testing.T) {
	mustPass(t, CheckOpenAIChatCompletion([]byte(openAIAnswer)))

	problems := CheckOpenAIChatCompletion([]byte(`{"id":"","object":"chat.completion.chunk","choices":[{"index":1,"message":{"role":"user","content":"hi"},"finish_reason":"succeeded"}]}`))
	for _, want := range []string{"id is missing", `object = "chat.completion.chunk"`, "created is missing", "index is missing or not 0", `role = "user"`, `finish_reason = "succeeded"`, "usage is missing"} {
		mustHave(t, problems, want)
	}

	mustHave(t, CheckOpenAIChatCompletion([]byte(`{"id":"x","object":"chat.completion","created":1,"choices":[]}`)), "choices is empty")
	// The gateway's own {status,msg,data,data2} envelope is not a chat.completion.
	mustHave(t, CheckOpenAIChatCompletion([]byte(gatewayEnvelope)), "choices is empty")
}

func TestCheckOpenAIStream(t *testing.T) {
	mustPass(t, CheckOpenAIStream(openAIChunks(chunkRole, chunkTextA, chunkTextB, chunkStop)))

	// No sentinel: a client that waits for [DONE] would hang.
	frames := []Frame{{Data: chunkRole}, {Data: chunkTextA}}
	mustHave(t, CheckOpenAIStream(frames), `want the sentinel "data: [DONE]"`)

	// No text delta at all.
	mustHave(t, CheckOpenAIStream(openAIChunks(chunkRole, chunkStop)), "no frame carried delta.content")

	// A chunk that invents its own object name.
	mustHave(t, CheckOpenAIStream(openAIChunks(`{"object":"chat.completion","choices":[{"index":0,"delta":{"content":"x"}}]}`, chunkStop)), `want "chat.completion.chunk"`)

	// An unmapped finish reason.
	mustHave(t, CheckOpenAIStream(openAIChunks(chunkTextA, `{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"succeeded"}]}`)), `finish_reason = "succeeded"`)

	// Non-JSON payload in the middle of the stream.
	mustHave(t, CheckOpenAIStream(openAIChunks(chunkTextA, "not json", chunkStop)), "frame 1: response body is not a JSON object")
}

func TestCheckOpenAIToolCalls(t *testing.T) {
	mustPass(t, CheckOpenAIToolCalls([]byte(openAIToolAnswer), []string{"get_weather"}, 1))

	mustHave(t, CheckOpenAIToolCalls([]byte(openAIAnswer), []string{"get_weather"}, 1), `finish_reason = "stop"`)
	mustHave(t, CheckOpenAIToolCalls([]byte(openAIToolAnswer), []string{"get_time"}, 1), "not one of the requested tools")
	mustHave(t, CheckOpenAIToolCalls([]byte(openAIToolAnswer), []string{"get_weather"}, 2), "want at least 2")

	brokenArgs := strings.Replace(openAIToolAnswer, `"arguments":"{\"city\":\"Osaka\"}"`, `"arguments":"{city: Osaka"`, 1)
	if brokenArgs == openAIToolAnswer {
		t.Fatal("fixture rewrite failed")
	}
	mustHave(t, CheckOpenAIToolCalls([]byte(brokenArgs), []string{"get_weather"}, 1), "is not a JSON object")

	emptyArgs := strings.Replace(openAIToolAnswer, `"arguments":"{\"city\":\"Osaka\"}"`, `"arguments":""`, 1)
	if emptyArgs == openAIToolAnswer {
		t.Fatal("fixture rewrite failed")
	}
	mustHave(t, CheckOpenAIToolCalls([]byte(emptyArgs), []string{"get_weather"}, 1), "arguments is empty")
}

func TestCheckOpenAIStreamToolCalls(t *testing.T) {
	// Arguments split across three chunks, exactly as a provider streams them.
	frames := openAIChunks(
		`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"ci"}}]}}]}`,
		`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"Osaka\"}"}}]}}]}`,
		`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	mustPass(t, CheckOpenAIStreamToolCalls(frames))

	// A second call whose fragments carry no index cannot be routed to a call slot.
	unindexed := openAIChunks(
		`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_1","function":{"name":"get_weather","arguments":"{}"}}]}}]}`,
		`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_2","function":{"name":"get_time","arguments":"{}"}}]}}]}`,
		`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	mustHave(t, CheckOpenAIStreamToolCalls(unindexed), "carries no index")

	// Truncated arguments: the stream said tool_calls, but the JSON never closed.
	truncated := openAIChunks(
		`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":"{\"city\":\"Osa"}}]}}]}`,
		`{"id":"a","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	mustHave(t, CheckOpenAIStreamToolCalls(truncated), "is not a JSON object")

	mustHave(t, CheckOpenAIStreamToolCalls(openAIChunks(chunkTextA, chunkStop)), "carried no delta.tool_calls")
}

func TestCheckJSONObjectContent(t *testing.T) {
	body := `{"id":"a","object":"chat.completion","created":1,"choices":[{"index":0,"message":{"role":"assistant","content":"{\"name\":\"Monet\",\"age\":86,\"tags\":[\"impressionism\"]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	mustPass(t, CheckJSONObjectContent([]byte(body), JSONKeys...))
	mustHave(t, CheckJSONObjectContent([]byte(body), "missing_key"), `required key "missing_key"`)

	prose := strings.Replace(body, `{\"name\":\"Monet\",\"age\":86,\"tags\":[\"impressionism\"]}`, `Monet lived a long life.`, 1)
	mustHave(t, CheckJSONObjectContent([]byte(prose)), "is not a JSON object")

	array := strings.Replace(body, `{\"name\":\"Monet\",\"age\":86,\"tags\":[\"impressionism\"]}`, `[{\"name\":\"Monet\"}]`, 1)
	mustHave(t, CheckJSONObjectContent([]byte(array)), "is not a JSON object")
}

func TestCheckAnthropicMessage(t *testing.T) {
	mustPass(t, CheckAnthropicMessage([]byte(anthropicAnswer)))

	problems := CheckAnthropicMessage([]byte(`{"id":"","type":"chat.completion","role":"user","content":[{"type":"text","text":""}],"stop_reason":"completed","usage":{}}`))
	for _, want := range []string{`type = "chat.completion"`, "id is missing", `role = "user"`, "model is not echoed", "no content block carries text", `stop_reason = "completed"`, "usage.input_tokens is missing"} {
		mustHave(t, problems, want)
	}
}

func TestCheckAnthropicStream(t *testing.T) {
	ok := []Frame{
		{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_1"}}`},
		{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{Event: "ping", Data: `{"type":"ping"}`},
		{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Blue"}}`},
		{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"."}}`},
		{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
		{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`},
		{Event: "message_stop", Data: `{"type":"message_stop"}`},
	}
	mustPass(t, CheckAnthropicStream(ok))

	noStop := ok[:len(ok)-1]
	mustHave(t, CheckAnthropicStream(noStop), "never reached \"message_stop\"")
	mustHave(t, CheckAnthropicStream(noStop), "stream ended without message_stop")

	skewed := []Frame{ok[0], ok[3], ok[7]}
	mustHave(t, CheckAnthropicStream(skewed), `event sequence never reached "content_block_start"`)

	mismatch := []Frame{{Event: "message_start", Data: `{"type":"content_block_start"}`}, ok[1], ok[3], ok[4], ok[5], ok[6], ok[7]}
	mustHave(t, CheckAnthropicStream(mismatch), "but payload type is")

	errored := append([]Frame{ok[0], {Event: "error", Data: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`}}, ok[2:]...)
	mustHave(t, CheckAnthropicStream(errored), "stream errored: overloaded_error: Overloaded")

	noText := []Frame{ok[0], ok[1], ok[2], ok[5], ok[6], ok[7]}
	mustHave(t, CheckAnthropicStream(noText), "no content_block_delta carried a text_delta")
}

func TestErrorEnvelopes(t *testing.T) {
	openAI := `{"error":{"message":"model not found","type":"invalid_request_error","param":"model","code":"model_not_found"}}`
	mustPass(t, CheckOpenAIErrorEnvelope([]byte(openAI)))
	// param is allowed to be null; code is what SDKs branch on, so it is required.
	mustPass(t, CheckOpenAIErrorEnvelope([]byte(`{"error":{"message":"m","type":"t","param":null,"code":"c"}}`)))
	mustHave(t, CheckOpenAIErrorEnvelope([]byte(`{"error":{"message":"m","type":"t"}}`)), "error.code is absent")
	mustHave(t, CheckOpenAIErrorEnvelope([]byte(gatewayEnvelope)), "instead of the OpenAI error envelope")
	mustHave(t, CheckOpenAIErrorEnvelope([]byte("not found")), "error response is not JSON")

	anthropic := `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	mustPass(t, CheckAnthropicErrorEnvelope([]byte(anthropic)))
	mustHave(t, CheckAnthropicErrorEnvelope([]byte(`{"type":"error","error":{"type":"weird","message":""}}`)), `error.type = "weird"`)
	mustHave(t, CheckAnthropicErrorEnvelope([]byte(gatewayEnvelope)), "instead of the Anthropic error envelope")
	// The OpenAI answer is not an Anthropic envelope either: it carries no top-level
	// type:"error", which is the first thing an Anthropic SDK reads. (Its error.type
	// string happens to overlap, which is exactly why the check looks at both.)
	mustHave(t, CheckAnthropicErrorEnvelope([]byte(openAI)), `type = "", want "error"`)
}

func TestClientStreamParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			io.WriteString(w, ": keep-alive\n\nevent: bogus\ndata:"+chunkTextA+"\n\ndata: "+chunkStop+"\n\ndata: [DONE]\n\n")
		case "/v1/messages":
			// A non-event-stream answer to a stream request arrives as a body instead.
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, gatewayEnvelope)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/v1", HTTP: srv.Client()}
	ctx := context.Background()

	reply, err := c.ChatStream(ctx, map[string]any{"model": "x", "stream": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Frames) != 3 {
		t.Fatalf("frames = %d (%v), want 3 data payloads", len(reply.Frames), reply.Frames)
	}
	if reply.Frames[0].Event != "bogus" {
		t.Errorf("first frame event = %q, want the event: line it rode on", reply.Frames[0].Event)
	}
	if reply.Frames[2].Data != "[DONE]" {
		t.Errorf("last frame = %q, want the [DONE] sentinel", reply.Frames[2].Data)
	}

	reply, err = c.MessagesStream(ctx, map[string]any{"model": "x", "stream": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Frames) != 0 || !strings.Contains(string(reply.Body), "authentication required") {
		t.Errorf("non-SSE stream answer: frames=%v body=%s, want the raw body", reply.Frames, clip(string(reply.Body)))
	}
}

func TestClientSendsCredentialAndVersion(t *testing.T) {
	var gotAuth, gotVersion, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotVersion, gotPath = r.Header.Get("Authorization"), r.Header.Get("Anthropic-Version"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, anthropicAnswer)
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/v1", Key: "hzo_test", HTTP: srv.Client()}
	if _, err := c.Messages(context.Background(), map[string]any{"model": "x"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer hzo_test" {
		t.Errorf("Authorization = %q, want the Bearer credential", gotAuth)
	}
	if gotVersion != AnthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, AnthropicVersion)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", gotPath)
	}
}

func TestBaseURL(t *testing.T) {
	t.Setenv(BaseURLEnv, "")
	if got := BaseURL(); got != DefaultBase+"/v1" {
		t.Errorf("default base = %q, want %s/v1", got, DefaultBase)
	}
	for _, in := range []string{"https://stg.api.hanzo.ai", "https://stg.api.hanzo.ai/", "https://stg.api.hanzo.ai/v1"} {
		t.Setenv(BaseURLEnv, in)
		if got := BaseURL(); got != "https://stg.api.hanzo.ai/v1" {
			t.Errorf("BaseURL(%q) = %q, want https://stg.api.hanzo.ai/v1", in, got)
		}
	}
	if NewClient().Key != os.Getenv(KeyEnv) {
		t.Error("NewClient must read the credential from the environment")
	}
}

func mustPass(t *testing.T, problems []string) {
	t.Helper()

	if len(problems) > 0 {
		t.Errorf("conforming response reported %d problems: %s", len(problems), strings.Join(problems, "; "))
	}
}

func mustHave(t *testing.T, problems []string, want string) {
	t.Helper()

	for _, p := range problems {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Errorf("problems %q do not mention %q", strings.Join(problems, " | "), want)
}
