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
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Scenario is one identical request, sent to every family that can take it, on the
// wire format named in its id. Run returns the HTTP status it saw, a one-line detail
// for the report, and the contract violations — empty means the cell passes.
type Scenario struct {
	// Name is the matrix column: openai_* rides /v1/chat/completions, anthropic_*
	// rides /v1/messages with the same question behind a different body shape.
	Name string
	// NeedsChat and NeedsTools gate the cell on what the catalog says the family
	// serves, so an unsupported capability reads as "skip" rather than "fail".
	NeedsChat  bool
	NeedsTools bool

	Run func(ctx context.Context, c *Client, f Family) (status int, detail string, problems []string)
}

// Scenarios is the coverage set named in #138: sync, streaming SSE layout, tool
// calls (single, parallel, and the multi-turn role:"tool" follow-up), structured
// output in both json_object and json_schema form, on both API shapes.
var Scenarios = []Scenario{
	{Name: "openai_sync", NeedsChat: true, Run: openAISync},
	{Name: "openai_stream", NeedsChat: true, Run: openAIStream},
	{Name: "openai_tools", NeedsChat: true, NeedsTools: true, Run: openAITools},
	{Name: "openai_tools_parallel", NeedsChat: true, NeedsTools: true, Run: openAIParallelTools},
	{Name: "openai_tool_followup", NeedsChat: true, NeedsTools: true, Run: openAIToolFollowUp},
	{Name: "openai_json_object", NeedsChat: true, Run: openAIJSONObject},
	{Name: "openai_json_schema", NeedsChat: true, Run: openAIJSONSchema},
	{Name: "openai_stream_tools", NeedsChat: true, NeedsTools: true, Run: openAIStreamTools},
	{Name: "anthropic_sync", NeedsChat: true, Run: anthropicSync},
	{Name: "anthropic_stream", NeedsChat: true, Run: anthropicStream},
}

// PlainPrompt is the one question every family answers on every sync/stream cell.
const PlainPrompt = "Name one primary color. Answer in one short sentence."

// JSONKeys are the fields a structured-output answer must carry.
var JSONKeys = []string{"name", "age", "tags"}

const jsonPrompt = "Return JSON only, no prose: an object with keys name (string), age (number) and tags (array of strings), describing one famous painter."

// weatherTool and timeTool are the two functions every tool-call cell offers.
var weatherTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "get_weather",
		"description": "Current weather for a city.",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"city": map[string]any{"type": "string"}},
			"required":   []string{"city"},
		},
	},
}

var timeTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "get_time",
		"description": "Current time in a timezone.",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"timezone": map[string]any{"type": "string"}},
			"required":   []string{"timezone"},
		},
	},
}

const toolPrompt = "What is the weather in Osaka right now? Call the get_weather tool."

func openAISync(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	reply, err := c.Chat(ctx, map[string]any{
		"model": f.Model, "max_tokens": 64,
		"messages": []any{map[string]any{"role": "user", "content": PlainPrompt}},
	})
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOK(reply.Body)
	}
	return reply.Status, syncDetail(reply.Body), CheckOpenAIChatCompletion(reply.Body)
}

func openAIStream(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	reply, err := c.ChatStream(ctx, map[string]any{
		"model": f.Model, "stream": true, "max_tokens": 64,
		"messages": []any{map[string]any{"role": "user", "content": PlainPrompt}},
	})
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOK(reply.Body)
	}
	problems := []string{}
	if !strings.Contains(strings.ToLower(reply.ContentType), "text/event-stream") {
		problems = append(problems, fmt.Sprintf("stream answered Content-Type %q, want text/event-stream", reply.ContentType))
	}
	return reply.Status, fmt.Sprintf("%d sse frames", len(reply.Frames)), append(problems, CheckOpenAIStream(reply.Frames)...)
}

func openAITools(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	reply, err := c.Chat(ctx, chatToolBody(f.Model))
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOK(reply.Body)
	}
	return reply.Status, toolDetail(reply.Body), CheckOpenAIToolCalls(reply.Body, []string{"get_weather"}, 1)
}

func openAIParallelTools(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	body := chatToolBody(f.Model)
	body["tools"] = []any{weatherTool, timeTool}
	body["messages"] = []any{map[string]any{"role": "user", "content": "In one turn, call BOTH tools: the weather in Paris and the time in Tokyo."}}
	body["parallel_tool_calls"] = true

	reply, err := c.Chat(ctx, body)
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOK(reply.Body)
	}
	return reply.Status, toolDetail(reply.Body), CheckOpenAIToolCalls(reply.Body, []string{"get_weather", "get_time"}, 2)
}

// openAIToolFollowUp closes the loop with a role:"tool" turn, which is where a
// gateway that cannot serialize the tool result back to the provider fails.
func openAIToolFollowUp(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	reply, err := c.Chat(ctx, map[string]any{
		"model": f.Model, "max_tokens": 64,
		"messages": []any{
			map[string]any{"role": "user", "content": toolPrompt},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "call_qa_1", "type": "function",
				"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Osaka"}`},
			}}},
			map[string]any{"role": "tool", "tool_call_id": "call_qa_1", "content": `{"city":"Osaka","celsius":18,"condition":"clear"}`},
		},
		"tools": []any{weatherTool},
	})
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOK(reply.Body)
	}
	return reply.Status, syncDetail(reply.Body), CheckOpenAIChatCompletion(reply.Body)
}

func openAIJSONObject(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	reply, err := c.Chat(ctx, map[string]any{
		"model": f.Model, "max_tokens": 256,
		"messages":        []any{map[string]any{"role": "user", "content": jsonPrompt}},
		"response_format": map[string]any{"type": "json_object"},
	})
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOK(reply.Body)
	}
	return reply.Status, syncDetail(reply.Body), CheckJSONObjectContent(reply.Body, JSONKeys...)
}

func openAIJSONSchema(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	reply, err := c.Chat(ctx, map[string]any{
		"model": f.Model, "max_tokens": 256,
		"messages": []any{map[string]any{"role": "user", "content": jsonPrompt}},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name":   "painter",
			"strict": true,
			"schema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"age":  map[string]any{"type": "number"},
					"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": JSONKeys,
			},
		}},
	})
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOK(reply.Body)
	}
	return reply.Status, syncDetail(reply.Body), CheckJSONObjectContent(reply.Body, JSONKeys...)
}

// openAIStreamTools asserts the tool-call serialization survives streaming: fragments
// must reassemble into complete calls with parseable arguments.
func openAIStreamTools(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	body := chatToolBody(f.Model)
	body["stream"] = true

	reply, err := c.ChatStream(ctx, body)
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOK(reply.Body)
	}
	return reply.Status, fmt.Sprintf("%d sse frames", len(reply.Frames)), CheckOpenAIStreamToolCalls(reply.Frames)
}

func anthropicSync(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	reply, err := c.Messages(ctx, map[string]any{
		"model": f.Model, "max_tokens": 64,
		"messages": []any{map[string]any{"role": "user", "content": PlainPrompt}},
	})
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOKAnthropic(reply.Body)
	}
	return reply.Status, syncDetail(reply.Body), CheckAnthropicMessage(reply.Body)
}

func anthropicStream(ctx context.Context, c *Client, f Family) (int, string, []string) {
	ctx, cancel := deadline(ctx)
	defer cancel()

	reply, err := c.MessagesStream(ctx, map[string]any{
		"model": f.Model, "stream": true, "max_tokens": 64,
		"messages": []any{map[string]any{"role": "user", "content": PlainPrompt}},
	})
	if err != nil {
		return 0, "", []string{err.Error()}
	}
	if reply.Status != 200 {
		return reply.Status, "", nonOKAnthropic(reply.Body)
	}
	problems := []string{}
	if !strings.Contains(strings.ToLower(reply.ContentType), "text/event-stream") {
		problems = append(problems, fmt.Sprintf("stream answered Content-Type %q, want text/event-stream", reply.ContentType))
	}
	return reply.Status, fmt.Sprintf("%d sse frames", len(reply.Frames)), append(problems, CheckAnthropicStream(reply.Frames)...)
}

func chatToolBody(model string) map[string]any {
	return map[string]any{
		"model": model, "max_tokens": 256,
		"messages":    []any{map[string]any{"role": "user", "content": toolPrompt}},
		"tools":       []any{weatherTool},
		"tool_choice": "auto",
	}
}

// nonOK reports a failed chat call together with whether the error it carried was
// even in the documented envelope — that distinction is what makes the matrix useful.
func nonOK(raw []byte) []string {
	return append([]string{"request failed: " + clip(string(raw))}, prefix("error envelope: ", CheckOpenAIErrorEnvelope(raw))...)
}

func nonOKAnthropic(raw []byte) []string {
	return append([]string{"request failed: " + clip(string(raw))}, prefix("error envelope: ", CheckAnthropicErrorEnvelope(raw))...)
}

func prefix(with string, problems []string) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, with+p)
	}
	return out
}

// syncDetail names the finish reason and token counts so a passing cell is still
// auditable from the report alone.
func syncDetail(raw []byte) string {
	body, err := decode(raw)
	if err != nil {
		return ""
	}
	var detail []string
	if choices := arr(body, "choices"); len(choices) > 0 {
		if choice, _ := choices[0].(map[string]any); choice != nil {
			if fr := str(choice, "finish_reason"); fr != "" {
				detail = append(detail, "finish="+fr)
			}
		}
	}
	if got := str(body, "stop_reason"); got != "" {
		detail = append(detail, "stop="+got)
	}
	if usage, _ := body["usage"].(map[string]any); usage != nil {
		for _, k := range []string{"prompt_tokens", "completion_tokens", "input_tokens", "output_tokens"} {
			if n, ok := num(usage, k); ok {
				detail = append(detail, fmt.Sprintf("%s=%d", k, int64(n)))
			}
		}
	}
	return strings.Join(detail, " ")
}

func toolDetail(raw []byte) string {
	body, err := decode(raw)
	if err != nil {
		return ""
	}
	var names []string
	if choices := arr(body, "choices"); len(choices) > 0 {
		if choice, _ := choices[0].(map[string]any); choice != nil {
			if msg, _ := choice["message"].(map[string]any); msg != nil {
				for _, rawCall := range arr(msg, "tool_calls") {
					call, _ := rawCall.(map[string]any)
					fn, _ := call["function"].(map[string]any)
					if n := str(fn, "name"); n != "" {
						names = append(names, n)
					}
				}
			}
		}
	}
	return "calls=" + strings.Join(names, ",")
}

// Cell is one family x scenario outcome of the matrix.
type Cell struct {
	Family   string   `json:"family"`
	Model    string   `json:"model"`
	Scenario string   `json:"scenario"`
	Status   int      `json:"status"`
	Result   string   `json:"result"` // pass | fail | skip
	Problems []string `json:"problems,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// WriteReport drops the matrix as JSON at path and a markdown twin beside it, so a CI
// run can publish both without post-processing.
func WriteReport(path string, cells []Cell) error {
	raw, err := json.MarshalIndent(cells, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(strings.TrimSuffix(path, ".json")+".md", reportMarkdown(cells), 0o644)
}

func reportMarkdown(cells []Cell) []byte {
	var b strings.Builder
	b.WriteString("# Cross-model API compatibility matrix\n\n")
	b.WriteString("Identical requests per family over `/v1/chat/completions` and `/v1/messages`.\n\n")
	b.WriteString("| Family | Model | Scenario | HTTP | Result | Detail / problems |\n|---|---|---|---|---|---|\n")
	for _, c := range cells {
		note := c.Detail
		if len(c.Problems) > 0 {
			note = strings.Join(c.Problems, "; ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %s |\n", c.Family, c.Model, c.Scenario, c.Status, c.Result, note)
	}
	var failed []string
	for _, c := range cells {
		if c.Result == "fail" {
			failed = append(failed, c.Family+"/"+c.Scenario)
		}
	}
	fmt.Fprintf(&b, "\n%d cells, %d failing: %s\n", len(cells), len(failed), strings.Join(failed, ", "))
	return []byte(b.String())
}
