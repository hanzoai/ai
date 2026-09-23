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
	openai "github.com/hanzoai/go-openai"
)

// What each hand-written handler reads.
//
// answers.go is what a handler writes back; this is the body it decodes. Without
// it the model calls reached the published document with a response and no
// request, so every generated client's completion method took no body and a
// first call through an SDK had nowhere to put the model or the messages.
//
// A handler absent from the table reads no JSON body of one fixed shape.
func Takes() map[string]any { return takes }

var takes = map[string]any{
	"ChatCompletions":       openai.ChatCompletionRequest{},
	"ChatCompletionsPublic": openai.ChatCompletionRequest{},
	"AnthropicMessages":     AnthropicRequest{},
	// The handler reads only the model and forwards the rest verbatim; this is the
	// OpenAI embedding request it forwards.
	"Embeddings": openai.EmbeddingRequest{},
}
