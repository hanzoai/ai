// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

// effort.go — the question Enso's router asks Kai: how much reasoning a request
// needs, as one choice whose labels are the effort ladder's words.
//
// The router asks Kai only for a request that left its effort to it (none stated, or
// auto). Kai answers on the Decisions wire (POST {KAI_URL}/v1/decisions):
//
//	{"model": "kai",
//	 "state": {"request": "<the last user turn, at most 4,000 characters>",
//	           "turns": <messages>, "tools": <tools offered>, "tokens": <prompt estimate>,
//	           "media": <whether it carries an image>, "task": "<router.Classify's task>"},
//	 "questions": {"effort": {"type": "choice",
//	   "instructions": "...",
//	   "criteria": {"instant": "...", "low": "...", "medium": "...", "high": "...", "max": "..."}}}}
//
// and its answer counts when answers.effort.answer_confidence is at Kai's floor
// (kaiFloor, the runtime's own qualified threshold). A Kai that is not configured,
// answers past kaiBudget, refuses, or is unsure leaves the router's own decision
// standing. The call is the router's, not the caller's: it is billed to no one.

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/ai/router"
)

const (
	// kaiFloor is the confidence Kai's answer counts at: the decision runtime's
	// qualified floor (decision_core::policy::FLOOR).
	kaiFloor = 0.9
	// kaiBudget is the longest the router waits for Kai before it serves on its own
	// decision. Kai's single-question read measured 172 ms at the median and 1,122 ms
	// at p95 on the production CPU path (Kai v0.4.32 release evidence); 600 ms covers
	// the median three times over and most of the tail, and costs a request that Kai
	// cannot judge in time no more than a slow first token would.
	kaiBudget = 600 * time.Millisecond
	// kaiText is the most of the last user turn Kai is shown.
	kaiText = 4000
)

// effortCriteria are the effort question's labels, the ladder's words, each with
// what it means. Kai's effort decisions are trained against these labels.
var effortCriteria = map[string]string{
	EffortInstant: "No reasoning: a greeting, a lookup, a rewrite, a one-line answer.",
	"low":         "Light reasoning: a short explanation or a small routine edit.",
	"medium":      "Ordinary multi-step work: a routine coding task, or an analysis of a few steps.",
	"high":        "Hard multi-step work: a non-trivial proof, a change across several files, a plan with dependencies.",
	"max":         "The hardest work: research-level mathematics, a deep debugging session, a long agentic task.",
}

const effortInstructions = "How much reasoning does a model need to answer this request well? Choose the least that will do."

// asked is what the router reads of a request: its classifier's input, and what
// Kai is shown beside it.
type asked struct {
	req          router.Request
	turns, tools int
}

// routerAsk reads a request body in any dialect a router id is sent in (chat
// `messages`, Anthropic `messages` and `system`, Responses `input`): its last user
// turn's text, how many turns and tools it carries, whether it holds an image, and a
// prompt estimate of about 4 characters a token over every turn's text.
func routerAsk(body []byte) asked {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
		Tools    []json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(body, &req)
	turns := req.Messages
	if len(turns) == 0 && len(req.Input) > 0 {
		var text string
		if json.Unmarshal(req.Input, &text) == nil {
			b, _ := json.Marshal(map[string]string{"role": "user", "content": text})
			turns = []json.RawMessage{b}
		} else {
			_ = json.Unmarshal(req.Input, &turns)
		}
	}
	a := asked{turns: len(turns), tools: len(req.Tools)}
	chars, last := 0, false
	for i := len(turns) - 1; i >= 0; i-- {
		var t struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(turns[i], &t) != nil {
			continue
		}
		text, media := partsOf(t.Content)
		chars += len(text)
		if !last && (t.Role == "" || t.Role == "user") {
			a.req.Text, a.req.HasMedia, last = text, media, true
		}
	}
	a.req.ApproxTokens = chars/4 + 1
	return a
}

// partsOf is a turn's content as text, and whether a part of it is an image: a
// string, or parts of type text / input_text, and image_url / input_image / image.
func partsOf(content json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text, false
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(content, &parts)
	var out []string
	media := false
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text":
			out = append(out, p.Text)
		case "image_url", "input_image", "image":
			media = true
		}
	}
	return strings.Join(out, "\n"), media
}

// effortQuestion is the body the router sends Kai for a request it read as task.
func effortQuestion(a asked, task string) []byte {
	text := a.req.Text
	if len(text) > kaiText {
		text = text[:kaiText]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	criteria := make(map[string]string, len(effortCriteria))
	for k, v := range effortCriteria {
		criteria[k] = v
	}
	b, _ := json.Marshal(map[string]any{
		"model": "kai",
		"state": map[string]any{
			"request": text, "turns": a.turns, "tools": a.tools,
			"tokens": a.req.ApproxTokens, "media": a.req.HasMedia, "task": task,
		},
		"questions": map[string]any{
			"effort": map[string]any{"type": "choice", "instructions": effortInstructions, "criteria": criteria},
		},
	})
	return b
}

// askKai sends Kai a decision body and answers what it said, ok false when Kai is not
// configured or did not answer 200. A seam, so a test states Kai's answer.
var askKai = func(ctx context.Context, org, rid string, body []byte) ([]byte, bool) {
	kai := kaiFor("kai")
	if kai == nil || strings.TrimSpace(kai.ProviderUrl) == "" {
		return nil, false
	}
	d := send(ctx, kai, org, "", rid, body)
	if d.fault != nil || d.status != 200 {
		return nil, false
	}
	return d.body, true
}

// kaiEffort is Kai's effort decision for a request: the ladder word it chose, ok only
// when it answered within kaiBudget at kaiFloor or above with one of the question's
// labels.
func kaiEffort(ctx context.Context, org, rid string, a asked, task string) (string, bool) {
	if strings.TrimSpace(a.req.Text) == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, kaiBudget)
	defer cancel()
	b, ok := askKai(ctx, org, rid, effortQuestion(a, task))
	if !ok {
		return "", false
	}
	var out struct {
		Answers map[string]struct {
			Choice           string   `json:"choice"`
			AnswerConfidence *float64 `json:"answer_confidence"`
		} `json:"answers"`
	}
	if json.Unmarshal(b, &out) != nil {
		return "", false
	}
	e, ok := out.Answers["effort"]
	if !ok || e.AnswerConfidence == nil || *e.AnswerConfidence < kaiFloor {
		return "", false
	}
	if _, known := effortCriteria[e.Choice]; !known {
		return "", false
	}
	return e.Choice, true
}
