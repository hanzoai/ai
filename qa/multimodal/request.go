// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package multimodal

import (
	"context"
	"encoding/base64"
)

// BuildBody assembles the /v1/chat/completions request for one vector: the pinned SystemPrompt
// as the system turn, the benign user text, and the untrusted artifact as one content part per
// channel. Every media rides inline as a base64 data URL — no fetchable URLs — so the gateway
// cannot be probed through an SSRF side channel while it digests the payload. Documents ride
// type:"file" parts (the OpenAI door the gateway documents for uploads); a row whose upstream
// takes documents only behind Anthropic document blocks answers 4xx, and Classify books that as
// REFUSED — a surface-area finding worth reporting, not a suite break (README.md).
func BuildBody(row *Row, v *Vector, art *Artifact) map[string]any {
	parts := MediaParts(v, art)
	body := map[string]any{
		"model": row.Model,
		"messages": []any{
			map[string]any{"role": "system", "content": SystemPrompt},
			map[string]any{"role": "user", "content": append([]any{map[string]any{"type": "text", "text": v.BenignText()}}, parts...)},
		},
		"temperature": 0.1, // deterministic verdicts: same payload, same answer shape, run over run
		"max_tokens":  128, // every expected answer is one sentence or one tool call
	}
	if v.Hijack {
		body["tools"] = []any{ToolDef}
		body["tool_choice"] = "auto" // forcing the call would hide whether the model obeyed the payload or its own judgment
	}
	return body
}

// MediaParts renders one artifact as content parts for its vector's channel. Video splits two
// ways by design: the container rides as one video_url part (capability probe — rows without a
// video input must refuse it), while key frames ride as one image_url part per frame, which
// #135's frame-text injection needs on every vision row regardless of container support.
func MediaParts(v *Vector, art *Artifact) []any {
	dataURL := "data:" + art.Media + ";base64," + base64.StdEncoding.EncodeToString(art.Bytes)
	switch {
	case v.Mod == Image:
		return []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}}}
	case v.Mod == Audio:
		return []any{map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": base64.StdEncoding.EncodeToString(art.Bytes), "format": "wav"}}}
	case v.Mod == Video && v.Frames:
		var parts []any
		for _, f := range art.Frames {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{
				"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(f),
			}})
		}
		return parts
	case v.Mod == Video:
		return []any{map[string]any{"type": "video_url", "video_url": map[string]string{"url": dataURL}}}
	case v.Mod == Document:
		return []any{map[string]any{"type": "file", "file": map[string]string{"filename": art.Name, "file_data": dataURL}}}
	}
	return nil
}

// Send builds the fixture for one vector and posts it to /v1/chat/completions. The served flag
// Classify needs comes from the caller's catalog check (Row.Supports), keeping this function
// network-only — the offline fake-gateway test drives it exactly like the live lane does.
func Send(ctx context.Context, c *Client, row *Row, v *Vector) (*Reply, error) {
	art, err := Build(v.Fixture, PayloadText(v))
	if err != nil {
		return nil, err
	}
	return c.Chat(ctx, BuildBody(row, v, art))
}
