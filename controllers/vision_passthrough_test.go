// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//      http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"encoding/json"
	"testing"

	openai "github.com/hanzoai/go-openai"
)

// The vision fix hinges on one predicate per endpoint: a request that carries a
// non-text content part must take the verbatim upstream path (which preserves the
// image), not the text-only QueryText pipeline (which drops it). These tests pin the
// two detectors so a regression can't silently re-break vision on either endpoint.

func TestRequestHasMedia_OpenAI(t *testing.T) {
	img := openai.ChatCompletionRequest{Messages: []openai.ChatCompletionMessage{{
		Role: "user",
		MultiContent: []openai.ChatMessagePart{
			{Type: openai.ChatMessagePartTypeText, Text: "what is this?"},
			{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{
				URL: "data:image/png;base64,iVBORw0KGgo=",
			}},
		},
	}}}
	if !requestHasMedia(&img) {
		t.Error("image MultiContent must be detected as media -> verbatim forward")
	}

	textArr := openai.ChatCompletionRequest{Messages: []openai.ChatCompletionMessage{{
		Role:         "user",
		MultiContent: []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: "hi"}},
	}}}
	if requestHasMedia(&textArr) {
		t.Error("text-only MultiContent must NOT be treated as media")
	}

	plain := openai.ChatCompletionRequest{Messages: []openai.ChatCompletionMessage{{
		Role: "user", Content: "hello",
	}}}
	if requestHasMedia(&plain) {
		t.Error("plain string content must NOT be treated as media")
	}
}

func TestRequestHasMediaAnthropic(t *testing.T) {
	msg := func(content string) AnthropicMessage {
		return AnthropicMessage{Role: "user", Content: json.RawMessage(content)}
	}
	imageReq := AnthropicRequest{Messages: []AnthropicMessage{msg(
		`[{"type":"text","text":"what is this?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]`)}}
	if !requestHasMediaAnthropic(&imageReq) {
		t.Error("anthropic image block must be detected as media -> verbatim forward")
	}

	textArr := AnthropicRequest{Messages: []AnthropicMessage{msg(`[{"type":"text","text":"hi"}]`)}}
	if requestHasMediaAnthropic(&textArr) {
		t.Error("anthropic text-only block array must NOT be treated as media")
	}

	strContent := AnthropicRequest{Messages: []AnthropicMessage{msg(`"hello"`)}}
	if requestHasMediaAnthropic(&strContent) {
		t.Error("anthropic string content must NOT be treated as media")
	}
}
