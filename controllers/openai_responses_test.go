package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestResponsesZstdPureGoRoundTrip(t *testing.T) {
	w, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := w.EncodeAll([]byte(`{"model":"zen4-coder","input":"hello"}`), nil)
	w.Close()

	decoded, err := decodeResponsesZstd(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(decoded), `{"model":"zen4-coder","input":"hello"}`; got != want {
		t.Fatalf("decoded = %q, want %q", got, want)
	}
}

func TestResponsesInputToMessagesToolRoundTrip(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"run it"}]},
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell_command","arguments":"{\"command\":\"pwd\"}"},
		{"type":"function_call_output","call_id":"call_1","output":"/tmp"},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
	]`)
	messages, err := responsesInputToMessages("be precise", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 {
		t.Fatalf("messages = %#v, want system + user + assistant tool call + tool result + assistant", messages)
	}
	if messages[0].Role != "system" || messages[0].Content != "be precise" {
		t.Fatalf("system message = %#v", messages[0])
	}
	if messages[2].Role != "assistant" || len(messages[2].ToolCalls) != 1 {
		t.Fatalf("tool call history = %#v", messages[2])
	}
	if got := messages[2].ToolCalls[0].Function.Name; got != "shell_command" {
		t.Fatalf("tool name = %q", got)
	}
	if messages[3].Role != "tool" || messages[3].ToolCallID != "call_1" || messages[3].Content != "/tmp" {
		t.Fatalf("tool result = %#v", messages[3])
	}
}

func TestResponsesToChatRequestTools(t *testing.T) {
	parallel := true
	req := &OpenAIResponsesRequest{
		Model: "zen4-coder", Input: json.RawMessage(`"hello"`), Stream: true,
		ParallelToolCalls: &parallel,
		ToolChoice:        json.RawMessage(`{"type":"function","name":"shell_command"}`),
		Tools: []responsesTool{{
			Type: "function", Name: "shell_command", Description: "run shell",
			Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		}},
	}
	chat, kinds, err := responsesToChatRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Tools) != 1 || chat.Tools[0].Function.Name != "shell_command" {
		t.Fatalf("tools = %#v", chat.Tools)
	}
	if kinds["shell_command"] != "function" {
		t.Fatalf("tool kinds = %#v", kinds)
	}
	choice, ok := chat.ToolChoice.(map[string]interface{})
	if !ok || choice["type"] != "function" {
		t.Fatalf("tool choice = %#v", chat.ToolChoice)
	}
	if enabled, ok := chat.ParallelToolCalls.(bool); !ok || !enabled {
		t.Fatalf("parallel_tool_calls = %#v", chat.ParallelToolCalls)
	}
}

func TestResponsesStreamTextWire(t *testing.T) {
	req := &OpenAIResponsesRequest{Model: "zen4-coder", Stream: true}
	recorder := httptest.NewRecorder()
	bridge := newResponsesBridgeWriter(recorder, req, nil)
	bridge.WriteHeader(http.StatusOK)

	upstream := strings.Join([]string{
		`data: {"id":"chat_1","model":"qwen","choices":[{"delta":{"role":"assistant","content":"HANZO_"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","model":"qwen","choices":[{"delta":{"content":"OK"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","model":"qwen","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	// Split in the middle of a frame to prove arbitrary network writes work.
	cut := len(upstream) / 2
	if _, err := bridge.Write([]byte(upstream[:cut])); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Write([]byte(upstream[cut:])); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}

	wire := recorder.Body.String()
	assertInOrder(t, wire,
		`event: response.created`,
		`event: response.output_item.added`,
		`event: response.content_part.added`,
		`"delta":"HANZO_"`,
		`"delta":"OK"`,
		`event: response.output_item.done`,
		`event: response.completed`,
	)
	for _, forbidden := range []string{`chat.completion.chunk`, `"choices"`, `data: [DONE]`} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("raw Chat Completions token %q leaked:\n%s", forbidden, wire)
		}
	}
	if !strings.Contains(wire, `"text":"HANZO_OK"`) || !strings.Contains(wire, `"total_tokens":14`) {
		t.Fatalf("missing final text/usage:\n%s", wire)
	}
}

func TestResponsesStreamFunctionCallWire(t *testing.T) {
	req := &OpenAIResponsesRequest{Model: "zen4-coder", Stream: true}
	recorder := httptest.NewRecorder()
	bridge := newResponsesBridgeWriter(recorder, req, map[string]string{"shell_command": "function"})
	bridge.WriteHeader(http.StatusOK)

	upstream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell_command","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"pwd\"}"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	if _, err := bridge.Write([]byte(upstream)); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}

	wire := recorder.Body.String()
	assertInOrder(t, wire,
		`event: response.created`,
		`event: response.output_item.added`,
		`event: response.function_call_arguments.delta`,
		`event: response.function_call_arguments.done`,
		`event: response.output_item.done`,
		`event: response.completed`,
	)
	for _, required := range []string{`"type":"function_call"`, `"call_id":"call_1"`, `"name":"shell_command"`, `"arguments":"{\"command\":\"pwd\"}"`} {
		if !strings.Contains(wire, required) {
			t.Fatalf("missing %q:\n%s", required, wire)
		}
	}
}

func TestResponsesNonStreamingTranslation(t *testing.T) {
	req := &OpenAIResponsesRequest{Model: "zen4-coder"}
	recorder := httptest.NewRecorder()
	bridge := newResponsesBridgeWriter(recorder, req, nil)
	bridge.WriteHeader(http.StatusOK)
	chat := `{"id":"chat_1","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
	if _, err := bridge.Write([]byte(chat)); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["object"] != "response" || got["status"] != "completed" {
		t.Fatalf("response = %#v", got)
	}
	if strings.Contains(recorder.Body.String(), `"choices"`) {
		t.Fatalf("chat response leaked: %s", recorder.Body.String())
	}
}

func assertInOrder(t *testing.T, text string, needles ...string) {
	t.Helper()
	pos := 0
	for _, needle := range needles {
		next := strings.Index(text[pos:], needle)
		if next < 0 {
			t.Fatalf("missing/out-of-order %q after byte %d:\n%s", needle, pos, text)
		}
		pos += next + len(needle)
	}
}
