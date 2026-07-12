// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

package model

import "testing"

func TestGetContextLength(t *testing.T) {
	testModel := map[string]int{
		// gpt
		"gpt-3.5-turbo":     16385,
		"gpt-4":             8192,
		"gpt-4o-2024-08-06": 128000,
		"gpt-4o":            128000,
		"gpt-4o-mini":       128000,
		// deepseek
		"deepseek-r1-distill-llama-70b": 131072,
		"deepseek-r1-distill-qwen-7b":   8192,
		"deepseek-r1:70b":               131072,
		"deepseek-r1:671b":              65536,
		"deepseek-r1":                   65536,
		"Deepseek-R1":                   65536,
		"deepseek-chat":                 65536,
		"Deepseek-R1-Distill-Qwen-32B":  32768,
		// qwen
		"qwen-long":       1000000,
		"qwen3-235b-a22b": 131072,
		"qwen3-32b":       131072,
		// llama
		"meta-llama/Meta-Llama-3.1-70B-Instruct": 131072,
		// hunyuan
		"hunyuan-lite":  262144,
		"hunyuan-turbo": 32768,
		// step
		"step-1-flash": 8192,
		"step-1-32k":   32768,
		"step-2-16k":   16384,
		// doubao
		"Doubao-pro-32k": 32768,
		"Doubao-lite-4k": 4096,
		// ── GLM: the 1M window landed at GLM-4.5 and holds through GLM-5.x.
		// glm-5.2 is the model Hanzo serves for `best`; it MUST be 1M, not the
		// 16384 fallback (which dead-ends /compact and caps long CC sessions).
		"glm-5.2":  1048576,
		"glm-5.1":  1048576,
		"GLM-5.2":  1048576,
		"glm-4.5":  1048576,
		"glm-4-plus": 1048576,
		"glm-4":      131072,
		"glm-3-turbo": 131072,
		"glm-4V":     8192,
		// ── Kimi K2.x: 256K context.
		"kimi-k2.6": 262144,
		"kimi-k2.7": 262144,
		// ── Modern DO-AI models the legacy table predates must NOT collapse to
		// 16384. The safe floor is 131072 (every served model is ≥128K).
		"deepseek-v4-pro":   131072,
		"deepseek-v4-flash": 131072,
		"qwen3.5-397b-a17b": 131072,
		"some-unknown-future-model": 131072,
	}
	for modelName, gtcontextLength := range testModel {
		contextLength := getContextLength(modelName)
		if contextLength != gtcontextLength {
			t.Errorf("got wrong context length for %s. expect %d , but %d returned", modelName, gtcontextLength, contextLength)
		}
	}
}
