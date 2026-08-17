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
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/proxy"
	"github.com/hanzoai/go-openai"
)

const (
	// maxImageBytes bounds one fetched image. The body is held in memory and
	// base64-encoded into the prompt, and its size is chosen by whoever wrote
	// the URL.
	maxImageBytes = 8 << 20
	// maxImages bounds how many fetches one message can cause. A page of markup
	// would otherwise turn a single completion into a burst of outbound requests.
	maxImages = 8
)

// imgTag matches an HTML image tag and captures its src attribute, quoted or not.
var imgTag = regexp.MustCompile(`(?is)<img\s[^>]*?\bsrc\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)[^>]*>`)

// fetcher gets the images named by a message. Public refuses any address that is
// not globally routable, which is what a URL taken from message text needs.
var fetcher = proxy.Public

// images returns the data: URLs of the images a message carries, and the message
// with those tags removed. An image is taken only from the src of an <img> tag,
// because that is the marker this pipeline writes when a message has one; a URL
// sitting in prose or in a code block is text the model should read, not an
// address to fetch.
//
// A tag whose src is not a fetchable image, or whose fetch fails, is left in the
// message as written and contributes no image. There is no error to return: an
// image the model would have liked is worth less than the answer.
func images(message string) ([]string, string) {
	var res []string
	text := imgTag.ReplaceAllStringFunc(message, func(tag string) string {
		if len(res) >= maxImages {
			return tag
		}
		src := unquote(imgTag.FindStringSubmatch(tag)[1])
		data, err := fetchImage(src)
		if err != nil {
			log.Warn("vision: skipping image %s: %s", src, err.Error())
			return tag
		}
		res = append(res, data)
		return ""
	})
	return res, text
}

// fetchImage reads src and returns it as a data: URL.
func fetchImage(src string) (string, error) {
	ext, ok := imageExt(src)
	if !ok {
		return "", fmt.Errorf("not an image url: %q", src)
	}

	resp, err := fetcher.Get(src)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", src, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxImageBytes {
		return "", fmt.Errorf("%s: image is larger than %d bytes", src, maxImageBytes)
	}

	return fmt.Sprintf("data:image/%s;base64,%s", ext, base64.StdEncoding.EncodeToString(data)), nil
}

// imageExt returns the image extension src names and whether src is an address
// worth fetching: an absolute http or https URL whose path ends in an image
// extension.
//
// A src holding a placeholder — {s}.tile.example.org/{z}/{x}/{y}.png — names a
// family of URLs the browser fills in at run time, not an image. Braces cannot
// appear literally in a URL (RFC 3986 requires them encoded), so their presence
// is the template itself saying so.
func imageExt(src string) (string, bool) {
	src = strings.TrimSpace(src)
	if strings.ContainsAny(src, "{}") {
		return "", false
	}

	u, err := url.Parse(src)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}

	// The extension comes from the path, so a query string stays out of the
	// media type it names.
	switch ext := strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), ".")); ext {
	case "jpg", "jpeg", "png", "gif", "webp":
		return ext, true
	}
	return "", false
}

func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
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

// VisionModelForImages is the model to rewrite to when a request carries images
// and the current model cannot handle them. zen5-omni (kimi-k2.6, 1T multimodal)
// is the primary; zen3-omni (qwen3.5-397b-a17b) is the cheaper fallback.
const VisionModelForImages = "zen5-omni"

func OpenaiRawMessagesToGptVisionMessages(messages []*RawMessage) []openai.ChatCompletionMessage {
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

		imgs, messageText := images(message.Text)

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

		for _, img := range imgs {
			item.MultiContent = append(item.MultiContent, openai.ChatMessagePart{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL:    img,
					Detail: openai.ImageURLDetailAuto,
				},
			})
		}

		res = append(res, item)
	}
	return res
}
