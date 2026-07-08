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

// Package router turns a chat request into a concrete model id. It backs the
// virtual `auto`/`zen-router` model: classify the request into a coarse task,
// then pick the preferred servable model for that task. The classifier is a
// faithful port of hanzo-router's Rust Heuristic (engine/hanzo-router/
// src/classify.rs); the Client can instead consult a learned zen-router served
// by hanzo-engine, falling back to this heuristic on ANY error so `auto` works
// with zero extra infrastructure.
package router

import "strings"

// Task is the coarse task bucket a request is classified into. Values mirror the
// snake_case tags of hanzo-router's registry::Task so a learned classifier and
// this heuristic produce interchangeable labels.
type Task string

const (
	TaskCode        Task = "code"
	TaskReasoning   Task = "reasoning"
	TaskMath        Task = "math"
	TaskCreative    Task = "creative"
	TaskVision      Task = "vision"
	TaskLongContext Task = "long_context"
	TaskCheapChat   Task = "cheap_chat"
	TaskGeneral     Task = "general"
)

// Request is the value the router reasons about — extracted from the chat
// request by the caller so classification stays pure. Mirrors classify.rs's
// Request.
type Request struct {
	// Text is the concatenated user text (the last user turn is enough).
	Text string
	// ApproxTokens is the approximate prompt length in tokens (drives
	// long-context routing).
	ApproxTokens int
	// HasMedia is true when the request carries image/video input.
	HasMedia bool
	// TaskHint is an optional explicit task override; "" skips it.
	TaskHint Task
}

// Classify maps a request to a Task using keyword/length/media heuristics. This
// is a faithful port of hanzo-router's Heuristic::classify — same order, same
// keyword sets, same thresholds — so routing matches the Rust engine's rule path.
func Classify(req Request) Task {
	if req.TaskHint != "" {
		return req.TaskHint
	}
	if req.HasMedia {
		return TaskVision
	}
	if req.ApproxTokens >= 32000 {
		return TaskLongContext
	}
	t := strings.ToLower(req.Text)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	switch {
	case strings.Contains(t, "```") || has("code", "function", "bug", "compile", "stack trace", "refactor"):
		return TaskCode
	case has("prove", "theorem", "integral", "equation", "calculate", "solve for"):
		return TaskMath
	case has("why", "reason", "step by step", "analyze", "explain how", "trade-off"):
		return TaskReasoning
	case has("write a story", "poem", "creative", "imagine", "brainstorm"):
		return TaskCreative
	case req.ApproxTokens <= 16:
		return TaskCheapChat
	default:
		return TaskGeneral
	}
}
