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

package controllers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	openai "github.com/hanzoai/go-openai"
)

// sseStreamChunk is the subset of an OpenAI streaming chunk we parse to bill a
// streamed tool-call response: token usage (from the forced include_usage chunk)
// plus the delta content / tool-call arguments used for the tokenizer fallback.
type sseStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// streamCaptureUsage copies an upstream OpenAI-style SSE stream from r to w while
// capturing token usage (from the forced include_usage chunk) and accumulating
// the output text (content + tool-call arguments) for a tokenizer fallback. A
// forced usage-only chunk (usage present, no choices) is suppressed when the
// client did not request usage; otherwise its envelope is fixed up for SDK
// clients. This is the billing-critical core of the streaming tool path: it
// guarantees a streamed tool call yields real token counts to bill.
//
// mk is the door every event leaves through (envelope.go): each chunk goes out in
// our envelope, stamped from the one mark, so the id holds for the whole stream.
func streamCaptureUsage(r io.Reader, w io.Writer, flush func(), clientWantsUsage bool, strip *model.ReasoningStripper, mk *mark) (prompt, completion, total int, completionText string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
	var sb strings.Builder

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "data: ") {
			raw := strings.TrimPrefix(line, "data: ")
			if strings.TrimSpace(raw) != "[DONE]" {
				var chunk sseStreamChunk
				if json.Unmarshal([]byte(raw), &chunk) == nil {
					if chunk.Usage != nil {
						if chunk.Usage.PromptTokens > 0 {
							prompt = chunk.Usage.PromptTokens
						}
						if chunk.Usage.CompletionTokens > 0 {
							completion = chunk.Usage.CompletionTokens
						}
						if chunk.Usage.TotalTokens > 0 {
							total = chunk.Usage.TotalTokens
						}
					}
					for _, ch := range chunk.Choices {
						sb.WriteString(ch.Delta.Content)
						for _, tc := range ch.Delta.ToolCalls {
							sb.WriteString(tc.Function.Arguments)
						}
					}
					// Strip inline reasoning from the FORWARDED content only —
					// billing above already counted the original. Gated by a
					// non-nil stripper (reasoning-inlining upstreams only), so
					// every other stream is byte-for-byte unchanged.
					if strip != nil && len(chunk.Choices) > 0 {
						line = "data: " + applyReasoningStrip(raw, strip)
					}
					if chunk.Usage != nil && len(chunk.Choices) == 0 {
						if !clientWantsUsage {
							continue
						}
						// An SDK client reads a usage-only event as a chunk like any
						// other, so give it the rest of the envelope. The id and the
						// model are the stamp's to say, and it says them below.
						var usageChunk map[string]interface{}
						if json.Unmarshal([]byte(raw), &usageChunk) == nil {
							usageChunk["object"] = "chat.completion.chunk"
							usageChunk["created"] = time.Now().Unix()
							usageChunk["choices"] = []interface{}{}
							if fixed, err := json.Marshal(usageChunk); err == nil {
								line = "data: " + string(fixed)
							}
						}
					}
				}
			}
			// Last thing before the wire, so it covers the line whatever the rewrites
			// above left it as. `[DONE]` is not an object and passes through untouched.
			line = "data: " + string(mk.stamp([]byte(strings.TrimPrefix(line, "data: "))))
		}

		_, _ = fmt.Fprintf(w, "%s\n", line)
		if flush != nil {
			flush()
		}
	}
	return prompt, completion, total, sb.String()
}

const (
	// reserveCompletionFloor is the completion-token cap the QueryText pipeline
	// applies to EVERY model (model.ChatCompletionRequest) AND the ceiling an
	// UNCAPPED request's max_tokens is clamped to. The reservation always covers
	// at least this many completion tokens, so a request can never settle beyond
	// its hold — closing the single-request overdraft (R1b): the prior 1024-token
	// estimate under-reserved an uncapped request that then settled the ACTUAL
	// (larger) spend, driving the balance negative within one request.
	reserveCompletionFloor = 4096

	// maxReserveCompletionTokens bounds an absurd client max_tokens so the
	// reservation can't be driven arbitrarily large by a hostile request.
	maxReserveCompletionTokens = 32768
)

// clampMaxTokens is the upstream completion ceiling enforced on a request. A
// normal client cap (0 < mt <= maxReserveCompletionTokens) is preserved as-is; an
// UNCAPPED (<=0) or absurd (> max) request is clamped to reserveCompletionFloor so
// the proxied (tool/stream) upstream can never emit an unbounded completion. The
// caller MUST assign this back to request.MaxTokens before the upstream call.
func clampMaxTokens(maxTokens int) int {
	if maxTokens > 0 && maxTokens <= maxReserveCompletionTokens {
		return maxTokens
	}
	return reserveCompletionFloor
}

// reserveCompletionTokens is the completion-token count to RESERVE for: the larger
// of the (clamped) upstream ceiling and the QueryText pipeline's fixed cap. The
// QueryText pipeline does NOT thread the request's max_tokens (it caps at
// reserveCompletionFloor itself), so the reservation must cover that floor even
// when the client capped lower — guaranteeing actual <= reserve on EVERY path.
func reserveCompletionTokens(maxTokens int) int {
	if c := clampMaxTokens(maxTokens); c > reserveCompletionFloor {
		return c
	}
	return reserveCompletionFloor
}

// budgetHold is a per-request reservation against the shared balance ledger. It
// holds an upper-bound estimate before the upstream call and releases it (while
// applying the ACTUAL spend) when the request settles — so concurrent requests
// for one subject can never double-spend or drive the balance negative.
type budgetHold struct {
	subject string
	est     int64
	settled bool
}

// reserveBudget holds est cents against the caller's spendable balance BEFORE a
// paid request runs. It returns ok=false (gate the request) ONLY when the subject
// has a known ledger balance that cannot cover the estimate. When the subject is
// unknown to the ledger (billing disabled, exempt org, or balance not yet
// fetched) it does NOT gate — reservation only strengthens the existing balance
// gate, it never introduces a new fail-closed path of its own.
func reserveBudget(subject string, est int64) (*budgetHold, bool) {
	if subject == "" {
		return &budgetHold{}, true
	}
	if _, known := object.GlobalBalanceLedger.Available(subject); !known {
		return &budgetHold{}, true
	}
	if !object.GlobalBalanceLedger.Reserve(subject, est) {
		return nil, false
	}
	return &budgetHold{subject: subject, est: est}, true
}

// settle releases the hold and applies the ACTUAL spend (actualCents) to the
// ledger so subsequent reads reflect it immediately. Idempotent and nil-safe, so
// a deferred fail-safe settle(0) is harmless once a real settle has run.
func (h *budgetHold) settle(actualCents int64) {
	if h == nil || h.settled || h.subject == "" {
		return
	}
	object.GlobalBalanceLedger.Settle(h.subject, h.est, actualCents)
	h.settled = true
}

// estimatePromptTokens counts the prompt tokens for a chat request (tiktoken via
// the model package), falling back to a coarse character estimate.
func estimatePromptTokens(req *openai.ChatCompletionRequest) int {
	if n, err := model.OpenaiNumTokensFromMessages(req.Messages, req.Model); err == nil && n > 0 {
		return n
	}
	chars := 0
	for _, m := range req.Messages {
		chars += len(m.Content)
		for _, p := range m.MultiContent {
			chars += len(p.Text)
		}
	}
	return chars/4 + 1
}

// estimateRequestCostCents returns an UPPER-BOUND cost for a chat request: the
// prompt cost plus the cost of reserveCompletionTokens(maxTokens) — the worst-case
// completion the upstream can actually emit on any serving path. The balance gate
// reserves this so a request is admitted only when the spendable balance covers
// its worst case, and (with the max_tokens clamp at the call site) the actual
// settle can never exceed the reservation.
func estimateRequestCostCents(modelName string, promptTokens, maxTokens int) int64 {
	return calculateCostCents(modelName, promptTokens, reserveCompletionTokens(maxTokens))
}
