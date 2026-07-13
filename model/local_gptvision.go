// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

package model

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sashabaranov/go-openai"
)

func extractImagesURL(message string) ([]string, string) {
	message = strings.Replace(message, "&nbsp;", " ", -1)
	br := regexp.MustCompile(`<br\s*/?>`)
	message = br.ReplaceAllString(message, " ")

	imgURL := regexp.MustCompile(`http[s]?://\S+\.(jpg|jpeg|png|gif|webp)`)
	urls := imgURL.FindAllString(message, -1)
	quote := regexp.MustCompile(`\"$`)
	for i, url := range urls {
		urls[i] = quote.ReplaceAllString(url, "")
	}

	message = imgURL.ReplaceAllString(message, "")

	img := regexp.MustCompile(`<img[^>]+>`)
	message = img.ReplaceAllString(message, "")
	return urls, message
}

func getImageRefinedText(text string) (string, error) {
	ext := filepath.Ext(text)
	if ext != "" {
		ext = ext[1:]
	}

	resp, err := http.Get(text)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	base64Data := base64.StdEncoding.EncodeToString(data)
	res := fmt.Sprintf("data:image/%s;base64,%s", ext, base64Data)
	return res, nil
}

func IsVisionModel(subType string) bool {
	// upstream (DO AI) vision-capable models — the actual model IDs sent to the provider.
	// Zen-branded models are resolved to their upstream BEFORE this check (route resolution
	// translates zen3-vl → nemotron-nano-12b-v2-vl, etc.), so this includes both the DO AI
	// model IDs AND the zen aliases for the path where no route resolution runs.
	upstreamVision := map[string]bool{
		// OpenAI vision models
		"gpt-4o": true, "gpt-4o-2024-08-06": true, "gpt-4o-mini": true, "gpt-4o-mini-2024-07-18": true,
		"gpt-4.5-preview": true, "gpt-4.5-preview-2025-02-27": true, "gpt-4.1": true,
		"gpt-4.1-mini": true, "gpt-4.1-nano": true, "o1": true, "o1-pro": true, "o3": true, "o4-mini": true,
		// DO AI multimodal / VL models (catalog July 2026)
		"qwen3.5-397b-a17b":            true, // Alibaba Qwen 3.5 — multimodal
		"qwen3-coder-flash":            true, // Alibaba Qwen3 Coder Flash 30B — multimodal
		"alibaba-qwen3-32b":            true, // Alibaba Qwen3-32B — multimodal
		"nemotron-nano-12b-v2-vl":      true, // NVIDIA Nemotron Nano 12B v2 VL
		"nemotron-3-nano-omni":         true, // NVIDIA Nemotron 3 Nano Omni — hypermodal
		"nemotron-3-ultra-550b":        true, // NVIDIA Nemotron 3 Ultra — multimodal
		"nemotron-3-super-120b":        true, // NVIDIA Nemotron 3 Super — multimodal
		"gemma-4-31b-it":               true, // Google Gemma 4 — multimodal
		"kimi-k2.5":                    true, // Moonshot Kimi K2.5 — multimodal
		"kimi-k2.6":                    true, // Moonshot Kimi K2.6 — multimodal
		"ministral-3-8b-instruct-2512": true, // Mistral Ministral 3 8B — multimodal
		"mistral-3-14B":                true, // Mistral Ministral 3 14B — multimodal
		// Zen-branded vision aliases (catch before/after route resolution)
		"zen3-vl": true, "zen3-omni": true, "zen5-omni": true, "zen-vision": true,
		"zen3-vl-2b": true, "zen3-vl-8b": true, "zen3-vl-32b": true, "zen3-vl-235b-a22b": true,
		// Fireworks VL aliases
		"qwen3-vl-30b": true, "qwen3-vl-30b-a3b": true, "qwen3-vl-235b": true,
		"qwen3-vl-30b-a3b-instruct": true, "qwen3-vl-30b-a3b-thinking": true,
	}
	return upstreamVision[subType]
}

// hasImageContent reports whether any message in the request carries image content.
// OpenAI format: MultiContent with ChatMessagePartTypeImageURL parts.
// Anthropic format: messages with "image" content blocks.
func HasImageContent(req *openai.ChatCompletionRequest) bool {
	for _, m := range req.Messages {
		for _, part := range m.MultiContent {
			if part.Type == openai.ChatMessagePartTypeImageURL {
				return true
			}
		}
	}
	return false
}

// VisionModelForImages is the model to rewrite to when a request carries images
// and the current model cannot handle them. zen5-omni (kimi-k2.6, 1T multimodal)
// is the primary; zen3-omni (qwen3.5-397b-a17b) is the cheaper fallback.
const VisionModelForImages = "zen5-omni"

func OpenaiRawMessagesToGptVisionMessages(messages []*RawMessage) ([]openai.ChatCompletionMessage, error) {
	res := []openai.ChatCompletionMessage{}
	for _, message := range messages {
		var role string
		if message.Author == "AI" {
			role = openai.ChatMessageRoleAssistant
		} else if message.Author == "System" {
			role = openai.ChatMessageRoleSystem
		} else if message.Author == "Tool" {
			role = openai.ChatMessageRoleTool
		} else {
			role = openai.ChatMessageRoleUser
		}

		urls, messageText := extractImagesURL(message.Text)

		item := openai.ChatCompletionMessage{
			Role: role,
		}

		if role == openai.ChatMessageRoleTool {
			item.ToolCallID = message.ToolCallID
		} else if role == openai.ChatMessageRoleAssistant {
			if message.ToolCall.ID != "" {
				item.ToolCalls = []openai.ToolCall{message.ToolCall}
			} else {
				item.ToolCalls = nil
			}
		}

		if len(messageText) > 0 {
			item.MultiContent = []openai.ChatMessagePart{
				{
					Type: openai.ChatMessagePartTypeText,
					Text: messageText,
				},
			}
		}

		for _, url := range urls {
			imageText, err := getImageRefinedText(url)
			if err != nil {
				return []openai.ChatCompletionMessage{}, err
			}

			item.MultiContent = append(item.MultiContent, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL:    imageText,
					Detail: openai.ImageURLDetailAuto,
				},
			})
		}

		res = append(res, item)
	}
	return res, nil
}
