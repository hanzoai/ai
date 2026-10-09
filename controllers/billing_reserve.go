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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	openai "github.com/hanzoai/go-openai"
)

// sseStreamChunk is the subset of an OpenAI streaming chunk the relay reads to bill
// a streamed answer: whether it carries usage (the forced include_usage chunk), and
// the delta content / tool-call arguments the tokenizer falls back to.
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
	// Usage is read here only for whether it is there. Its counts are read by
	// sniffZenUsage, the one reader of usage in both dialects, because the split
	// between a fresh and a cached prompt token is what the price turns on.
	Usage *struct{} `json:"usage"`
}

// streamCaptureUsage copies an upstream OpenAI-style SSE stream from r to w while
// capturing token usage (from the forced include_usage chunk) and accumulating
// the output text (content + tool-call arguments) for a tokenizer fallback. A
// forced usage-only chunk (usage present, no choices) is suppressed when the
// client did not request usage; otherwise its envelope is fixed up for SDK
// clients. This is the billing-critical core of the streaming relay: it
// guarantees a streamed answer yields real token counts to bill.
//
// mk is what stamps every event on the way out (envelope.go): each chunk goes out in
// our envelope, stamped from the one mark, so the id holds for the whole stream.
//
// A line may be as long as the family relay allows (relayZenStream). A scanner that
// meets a longer one stops, and everything after it — the rest of the answer and
// the usage that bills it — would be dropped without an error anyone sees.
func streamCaptureUsage(r io.Reader, w io.Writer, flush func() error, clientWantsUsage bool, strip *model.ReasoningStripper, mk *mark) (t tokens, completionText string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var sb strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		// An SSE comment is a vendor keeping its connection open in its own words
		// (": OPENROUTER PROCESSING"). The keep-alive goes on; the words do not.
		if strings.HasPrefix(line, ":") {
			line = ":"
		}

		if after, ok := strings.CutPrefix(line, "data: "); ok {
			raw := after
			if strings.TrimSpace(raw) != "[DONE]" {
				var chunk sseStreamChunk
				if json.Unmarshal([]byte(raw), &chunk) == nil {
					if chunk.Usage != nil {
						sniffZenUsage([]byte(raw), &t)
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
						var usageChunk map[string]any
						if json.Unmarshal([]byte(raw), &usageChunk) == nil {
							usageChunk["object"] = "chat.completion.chunk"
							usageChunk["created"] = time.Now().Unix()
							usageChunk["choices"] = []any{}
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

		// A write that fails is the client gone: return, and the caller's close of r
		// hangs up on the vendor so its model stops. The text so far is what bills.
		if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
			break
		}
		if flush != nil && flush() != nil {
			break
		}
	}
	return t, sb.String()
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

// clampMaxTokens is the upstream completion ceiling enforced on a request for model.
// A normal client cap (0 < mt <= maxReserveCompletionTokens) is preserved as-is, and
// a larger one is lowered to maxReserveCompletionTokens, so the proxied (tool/stream)
// upstream can never emit an unbounded completion. A request that names no cap gets
// reserveCompletionFloor — except on a paid tier of Zen or Enso (thinks), whose
// models think before they answer and spend the ceiling on it: there a request that
// names none gets maxReserveCompletionTokens, or the answer is all reasoning, cut at
// `length`, and billed. Whatever the ceiling, it is never above the most one answer
// of model may hold where it is stated (mostOut; gpt-4o: 16,384), which its vendor
// would refuse. The caller MUST assign this back to request.MaxTokens before the
// upstream call.
func clampMaxTokens(model string, maxTokens int) int {
	c := reserveCompletionFloor
	switch {
	case maxTokens > maxReserveCompletionTokens:
		c = maxReserveCompletionTokens
	case maxTokens > 0:
		c = maxTokens
	case thinks(model):
		c = maxReserveCompletionTokens
	}
	if most := mostOut(model); most > 0 && c > most {
		c = most
	}
	return c
}

// mostOut is the most completion tokens one answer of model may hold, where it is
// stated: a family's catalog (OpenRouter's top_provider.max_completion_tokens) or a
// configured route's models.yaml max_output_tokens, following the configuration's
// aliases and each route's upstream model, however many steps, into a family's
// catalog where one ends there. 0 when nothing on the way states it.
func mostOut(model string) int {
	mc := GetModelConfig()
	seen := map[string]bool{}
	for m := strings.ToLower(strings.TrimSpace(model)); m != "" && !seen[m]; {
		seen[m] = true
		if f, ok := familyLookup(m); ok && f.MaxOut > 0 {
			return f.MaxOut
		}
		if mc == nil {
			return 0
		}
		n, next := mc.outStep(m)
		if n > 0 {
			return n
		}
		m = next
	}
	return 0
}

// thinks reports whether model is a paid tier of Zen or Enso: priced, so served by
// models that always reason. A model resold from another vendor's catalog is not
// one. Read from the discovery snapshot, never a refresh.
func thinks(model string) bool {
	for _, f := range []*modelFamily{zenFam, ensoFam} {
		if m, ok := f.lookup(model); ok {
			return m.priced()
		}
	}
	return false
}

// reserveCompletionTokens is the completion-token count to RESERVE for: the larger
// of the (clamped) upstream ceiling and the QueryText pipeline's fixed cap. The
// QueryText pipeline does NOT thread the request's max_tokens (it caps at
// reserveCompletionFloor itself), so the reservation must cover that floor even
// when the client capped lower — guaranteeing actual <= reserve on EVERY path.
func reserveCompletionTokens(model string, maxTokens int) int {
	if c := clampMaxTokens(model, maxTokens); c > reserveCompletionFloor {
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
	// settled is set by the one settle that runs. Atomic, because a streamed answer
	// settles from the goroutine producing it while the handler's deferred settle
	// may run at the same moment.
	settled atomic.Bool
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

// reserveFor is reserveBudget for the request on ctx: a request the plan or a free
// cap pays for holds nothing, because no wallet pays for it.
func reserveFor(ctx context.Context, subject string, est int64) (*budgetHold, bool) {
	if covered(ctx) {
		return &budgetHold{}, true
	}
	return reserveBudget(subject, est)
}

// settle releases the hold and applies the ACTUAL spend (actualCents) to the
// ledger so subsequent reads reflect it immediately. Idempotent and nil-safe, so
// a deferred fail-safe settle(0) is harmless once a real settle has run.
func (h *budgetHold) settle(actualCents int64) {
	h.settleNano(actualCents * 10_000_000)
}

// settleNano is settle with the actual spend in nano-USD, for a call priced below a
// cent: the ledger carries the fraction rather than rounding it away.
func (h *budgetHold) settleNano(actualNano int64) {
	if h == nil || h.subject == "" || !h.settled.CompareAndSwap(false, true) {
		return
	}
	object.GlobalBalanceLedger.SettleNano(h.subject, h.est, actualNano)
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
//
// A route priced at zero estimates zero, the same answer costsNothing gives the
// two wallet gates. Without this the worst case is a cent that will never be
// charged: calculateCostCents floors a non-zero token count at 1 cent so a real
// call is never billed as free, and on a $0.00 wallet that invented cent is the
// difference between admitted and refused — which put the free routes out of
// reach of the free tier they are published for. The floor still governs
// settlement, where it belongs.
//
// The price is read the way the arithmetic below reads it — no org, so the guard
// and the sum it guards can never disagree — and it fails closed like its twins:
// only a price that was FOUND and is zero estimates zero.
func estimateRequestCostCents(modelName string, promptTokens, maxTokens int) int64 {
	if costsNothing(modelName, "") {
		return 0
	}
	return calculateCostCents(modelName, promptTokens, reserveCompletionTokens(modelName, maxTokens))
}
