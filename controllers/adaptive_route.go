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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// adaptive_route.go — per-thread adaptive intelligence and cost-aware tier
// shifting for Enso's auto router (enso-auto / enso / hanzo/enso).
//
// Long-horizon coding and agentic sessions spend 70-85% of their turns on
// routine tool continuations (file reads, greps, clean test runs, edits) and
// short follow-ups after an initial planning turn. Keeping every turn pinned to
// enso-pro (10x enso-flash) or enso-max (13x) multiplies user bills without
// improving outcomes. Conversely, staying stuck on enso-flash when the model is
// looping on errors or the user is visibly frustrated ruins quality.
//
// This layer evaluates conversation history and bounded per-thread state to:
//   1. Shift DOWN aggressively to the shallowest tier (enso-flash) on clean
//      tool-result continuations and calm multi-turn follow-ups.
//   2. Step UP dynamically (flash -> pro -> max -> ultra) when user frustration,
//      corrections/pushback, explicit depth requests, or consecutive tool/build
//      failures are detected — escalating one notch higher if a prior step-up
//      still did not resolve the struggle.
//   3. Give genuinely complex turn-1 design/architecture prompts a smart start
//      while shifting down to enso-flash as soon as execution turns smooth out.

const (
	adaptiveCacheMax = 4096
	adaptiveCacheTTL = 30 * time.Minute
)

// threadSignals summarizes the routing-relevant cues extracted from a request's
// full message history (Chat Completions, Anthropic Messages, or Responses).
type threadSignals struct {
	ThreadKey                string
	UserTurns                int
	TotalTurns               int
	LastUserText             string
	TrailingToolContinuation bool
	TrailingToolError        bool
	ConsecutiveToolErrors    int
	FrustrationLevel         int // 0..3
	RoutineFollowUp          bool
	DeepInitialPrompt        bool
}

type threadAdaptiveState struct {
	lastTierIdx int
	cleanStreak int
	escalations int
	updatedAt   time.Time
}

type threadAdaptiveCache struct {
	mu      sync.Mutex
	entries map[string]threadAdaptiveState
}

var adaptiveThreads = &threadAdaptiveCache{
	entries: make(map[string]threadAdaptiveState, 256),
}

func (c *threadAdaptiveCache) get(key string, now time.Time) (threadAdaptiveState, bool) {
	if key == "" {
		return threadAdaptiveState{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.entries[key]
	if !ok {
		return threadAdaptiveState{}, false
	}
	if now.Sub(st.updatedAt) > adaptiveCacheTTL {
		delete(c.entries, key)
		return threadAdaptiveState{}, false
	}
	return st, true
}

func (c *threadAdaptiveCache) put(key string, st threadAdaptiveState, now time.Time) {
	if key == "" {
		return
	}
	st.updatedAt = now
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= adaptiveCacheMax {
		// Evict expired entries first; if still full, drop oldest half to stay O(1) amortized.
		for k, v := range c.entries {
			if now.Sub(v.updatedAt) > adaptiveCacheTTL {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= adaptiveCacheMax {
			cutoff := now.Add(-adaptiveCacheTTL / 2)
			for k, v := range c.entries {
				if v.updatedAt.Before(cutoff) {
					delete(c.entries, k)
				}
			}
		}
		if len(c.entries) >= adaptiveCacheMax {
			c.entries = make(map[string]threadAdaptiveState, 256)
		}
	}
	c.entries[key] = st
}

// resetAdaptiveThreads clears per-thread routing memory (for deterministic tests).
func resetAdaptiveThreads() {
	adaptiveThreads.mu.Lock()
	adaptiveThreads.entries = make(map[string]threadAdaptiveState, 256)
	adaptiveThreads.mu.Unlock()
}

// analyzeThreadSignals inspects the request body across OpenAI Chat, Anthropic
// Messages, and OpenAI Responses formats without extra network hops.
func analyzeThreadSignals(body []byte) threadSignals {
	var req struct {
		ThreadID           string            `json:"thread_id"`
		PreviousResponseID string            `json:"previous_response_id"`
		Metadata           map[string]string `json:"metadata"`
		Messages           []json.RawMessage `json:"messages"`
		Input              json.RawMessage   `json:"input"`
		Tools              []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(body, &req) != nil {
		return threadSignals{}
	}

	rawTurns := req.Messages
	if len(rawTurns) == 0 && len(req.Input) > 0 {
		var s string
		if json.Unmarshal(req.Input, &s) == nil {
			b, _ := json.Marshal(map[string]string{"role": "user", "content": s})
			rawTurns = []json.RawMessage{b}
		} else {
			_ = json.Unmarshal(req.Input, &rawTurns)
		}
	}

	sig := threadSignals{TotalTurns: len(rawTurns)}
	type parsedTurn struct {
		role        string
		turnType    string
		text        string
		isTool      bool
		isToolError bool
	}

	turns := make([]parsedTurn, 0, len(rawTurns))
	var firstUserText string
	var userTexts []string

	for _, raw := range rawTurns {
		var m struct {
			Role    string          `json:"role"`
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			Output  json.RawMessage `json:"output"`
			IsError bool            `json:"is_error"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		pt := parsedTurn{
			role:     strings.ToLower(strings.TrimSpace(m.Role)),
			turnType: strings.ToLower(strings.TrimSpace(m.Type)),
		}

		switch {
		case pt.role == "tool" || pt.turnType == "function_call_output":
			pt.isTool = true
			if len(m.Output) > 0 {
				pt.text, _ = partsOf(m.Output)
			} else {
				pt.text, _ = partsOf(m.Content)
			}
			pt.isToolError = m.IsError || looksLikeToolError(pt.text)
		default:
			text, toolOnly, toolErr := inspectMessageContent(m.Content)
			pt.text = text
			if toolOnly {
				pt.isTool = true
				pt.isToolError = toolErr
			} else if pt.role == "" || pt.role == "user" {
				sig.UserTurns++
				if firstUserText == "" {
					firstUserText = text
				}
				userTexts = append(userTexts, text)
				sig.LastUserText = text
			}
		}
		turns = append(turns, pt)
	}

	// Thread key: explicit id preferred, else deterministic fingerprint of initial user turn.
	for _, cand := range []string{
		req.ThreadID,
		req.PreviousResponseID,
		req.Metadata["thread_id"],
		req.Metadata["session_id"],
		req.Metadata["conversation_id"],
	} {
		if t := strings.TrimSpace(cand); t != "" {
			sig.ThreadKey = t
			break
		}
	}
	if sig.ThreadKey == "" && strings.TrimSpace(firstUserText) != "" {
		seed := strings.TrimSpace(firstUserText)
		if len(seed) > 512 {
			seed = seed[:512]
		}
		sum := sha256.Sum256([]byte(seed))
		sig.ThreadKey = "fp:" + hex.EncodeToString(sum[:8])
	}

	// Trailing tool continuation & consecutive tool error count.
	if n := len(turns); n > 0 && turns[n-1].isTool {
		sig.TrailingToolContinuation = true
		// Inspect the trailing burst of tool outputs (parallel tool calls share one step).
		i := n - 1
		for i >= 0 && turns[i].isTool {
			if turns[i].isToolError {
				sig.TrailingToolError = true
			}
			i--
		}
	}

	// Count consecutive failing tool steps walking backward across recent assistant/tool steps.
	for i := len(turns) - 1; i >= 0; {
		if turns[i].isTool {
			stepErr := false
			for i >= 0 && turns[i].isTool {
				if turns[i].isToolError {
					stepErr = true
				}
				i--
			}
			if stepErr {
				sig.ConsecutiveToolErrors++
				continue
			}
			break
		}
		if turns[i].role == "assistant" {
			i--
			continue
		}
		// Stop once we cross back over a human user turn.
		break
	}

	// Frustration & escalation detection on the latest human user turn (and recent turns).
	if len(userTexts) > 0 {
		lastIdx := len(userTexts) - 1
		lvl := frustrationScore(userTexts[lastIdx], sig.UserTurns)
		// If the user already pushed back earlier in the last 3 user turns, add +1 escalation.
		if lvl > 0 && lastIdx >= 1 {
			start := lastIdx - 2
			if start < 0 {
				start = 0
			}
			for j := start; j < lastIdx; j++ {
				if frustrationScore(userTexts[j], sig.UserTurns) > 0 {
					lvl++
					break
				}
			}
		}
		if lvl > 3 {
			lvl = 3
		}
		sig.FrustrationLevel = lvl
	}

	// Routine follow-up vs deep turn-1 prompt detection.
	if sig.FrustrationLevel == 0 && sig.ConsecutiveToolErrors == 0 {
		if sig.TrailingToolContinuation && !sig.TrailingToolError {
			sig.RoutineFollowUp = true
		} else if sig.UserTurns >= 2 && isCalmRoutineFollowUp(sig.LastUserText) {
			sig.RoutineFollowUp = true
		}
	}

	if sig.UserTurns == 1 && !sig.TrailingToolContinuation {
		sig.DeepInitialPrompt = isDeepInitialPrompt(sig.LastUserText, len(req.Tools))
	}

	return sig
}

// inspectMessageContent extracts text from a message content field and reports
// whether it consists exclusively of Anthropic tool_result blocks (and whether
// any tool_result signaled an error).
func inspectMessageContent(content json.RawMessage) (text string, toolOnly bool, toolErr bool) {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s, false, false
	}
	var parts []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
		IsError bool            `json:"is_error"`
	}
	if json.Unmarshal(content, &parts) != nil || len(parts) == 0 {
		return "", false, false
	}
	var out []string
	toolResults := 0
	nonTool := 0
	for _, p := range parts {
		switch p.Type {
		case "tool_result":
			toolResults++
			t, _ := partsOf(p.Content)
			if t == "" {
				t = p.Text
			}
			out = append(out, t)
			if p.IsError || looksLikeToolError(t) {
				toolErr = true
			}
		case "text", "input_text":
			nonTool++
			out = append(out, p.Text)
		default:
			nonTool++
		}
	}
	return strings.Join(out, "\n"), toolResults > 0 && nonTool == 0, toolErr
}

var toolErrorMarkers = []string{
	"exit status ",
	"exited with code ",
	"--- fail:",
	"fail\t",
	"panic:",
	"traceback (most recent call last)",
	"compilation failed",
	"command failed",
	"syntaxerror:",
	"typeerror:",
	"referenceerror:",
	"error[e",
	"build failed",
	"fatal error:",
	"undefined:",
}

func looksLikeToolError(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	// Check head and tail (build/test outputs place summaries at the end).
	sample := text
	if len(sample) > 4000 {
		sample = sample[:1500] + "\n" + sample[len(sample)-2500:]
	}
	lower := strings.ToLower(sample)
	// Explicit "exit code 0" / "exited with code 0" is a success, not an error.
	if strings.Contains(lower, "exited with code 0") || strings.Contains(lower, "exit status 0") {
		return false
	}
	for _, marker := range toolErrorMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

var (
	pushbackCues = []string{
		"that's wrong", "that is wrong", "thats wrong",
		"you're wrong", "you are wrong", "youre wrong",
		"not right", "incorrect",
		"didn't work", "did not work", "didnt work",
		"doesn't work", "does not work", "doesnt work",
		"not working", "still broken", "still failing", "still fails",
		"still getting", "same error", "same issue", "same bug",
		"try again", "you broke", "you missed", "wrong file",
		"hallucinat", "stop doing", "read carefully",
		"i already told you", "not what i asked", "failed again",
		"fix this properly", "actually fix", "that broke",
	}
	depthDemandCues = []string{
		"think harder", "think carefully", "really think",
		"use a better model", "use a smarter model", "smarter model",
		"deep dive", "double check", "be thorough", "root cause",
	}
	strongFrustrationCues = []string{
		"wtf", "wth", "fuck", "fucking", "shit",
		"broken again", "useless", "are you kidding",
		"seriously?", "how many times", "stop hallucinating",
	}
	negativeWords = []string{
		"wrong", "broken", "fail", "fix", "stop", "why", "still", "bug", "error",
	}
)

func frustrationScore(text string, userTurns int) int {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return 0
	}
	score := 0
	hasAny := func(cues []string) bool {
		for _, c := range cues {
			if strings.Contains(t, c) {
				return true
			}
		}
		return false
	}

	if hasAny(pushbackCues) {
		score++
	} else if userTurns >= 2 {
		for _, pfx := range []string{"no, ", "nope, ", "nope ", "wrong, ", "wrong!"} {
			if strings.HasPrefix(t, pfx) && len(t) <= 300 {
				score++
				break
			}
		}
	}

	if hasAny(depthDemandCues) {
		score++
	}

	if hasAny(strongFrustrationCues) {
		score++
	} else if (strings.Contains(t, "!!") || strings.Contains(t, "??")) && hasAny(negativeWords) {
		score++
	}

	if score > 3 {
		score = 3
	}
	return score
}

func isCalmRoutineFollowUp(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return true
	}
	if len(t) > 240 || strings.Contains(t, "```") {
		return false
	}
	// Do not shift down if the user explicitly asks for formal math/proof or architecture.
	for _, heavy := range []string{
		"prove", "theorem", "architecture", "design doc", "migration plan",
		"security audit", "race condition", "deadlock", "algorithm",
	} {
		if strings.Contains(t, heavy) {
			return false
		}
	}
	return true
}

func isDeepInitialPrompt(text string, tools int) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	// Explicit architecture/hard-systems/formal-proof signals on turn 1.
	for _, cue := range []string{
		"architecture", "design doc", "migration plan", "security audit",
		"race condition", "deadlock", "distributed consensus", "formal proof",
		"prove that", "prove step by step",
	} {
		if strings.Contains(t, cue) {
			return true
		}
	}
	// Long complex initial coding specification with tools attached.
	if len(t) >= 1200 && (tools > 0 || strings.Contains(t, "```")) {
		return true
	}
	return false
}

// applyAdaptiveRouting adjusts p.sku along the depth ladder using per-thread
// signals (clean tool/follow-up shift-down, frustration/struggle escalation,
// and smart turn-1 startup). Never runs on explicit caller effort (!p.auto).
func (p *depthPick) applyAdaptiveRouting(org string, sig threadSignals, kaiChose bool) {
	if !p.auto {
		return
	}
	cands := tiers(p.row)
	if len(cands) < 2 {
		return
	}
	ladder := make([]string, len(cands))
	indexOf := make(map[string]int, len(cands))
	for i, c := range cands {
		ladder[i] = c.Model
		indexOf[c.Model] = i
	}
	curIdx, ok := indexOf[p.sku]
	if !ok {
		curIdx = 0
	}

	now := time.Now()
	cacheKey := ""
	if sig.ThreadKey != "" {
		cacheKey = org + "|" + sig.ThreadKey
	}
	prev, hasPrev := adaptiveThreads.get(cacheKey, now)

	// Compute struggle boost from consecutive failing tool steps.
	// 1 failure is normal TDD/iteration; 2+ signals the current tier is struggling.
	struggleBoost := 0
	switch {
	case sig.ConsecutiveToolErrors >= 3:
		struggleBoost = 2
	case sig.ConsecutiveToolErrors == 2:
		struggleBoost = 1
	}

	boost := sig.FrustrationLevel + struggleBoost
	nextIdx := curIdx
	nextBy := p.by
	note := ""

	switch {
	case boost > 0:
		target := boost
		if hasPrev && prev.lastTierIdx > 0 && prev.escalations > 0 {
			// Prior escalated tier still hit frustration/failure: step one notch higher.
			if step := prev.lastTierIdx + 1; step > target {
				target = step
			}
		}
		if target >= len(ladder) {
			target = len(ladder) - 1
		}
		if target > nextIdx {
			nextIdx = target
			nextBy = "adaptive-escalate"
			switch {
			case sig.FrustrationLevel > 0 && struggleBoost > 0:
				note = "escalated after detecting user frustration and consecutive tool failures"
			case sig.FrustrationLevel > 0:
				note = "escalated after detecting user frustration or pushback"
			default:
				note = "escalated after consecutive tool failures"
			}
		}
		adaptiveThreads.put(cacheKey, threadAdaptiveState{
			lastTierIdx: nextIdx,
			cleanStreak: 0,
			escalations: prev.escalations + 1,
		}, now)

	case sig.RoutineFollowUp && (!kaiChose || sig.TrailingToolContinuation):
		// Shift aggressively down to the cheapest tier (enso-flash) on clean tool
		// continuations and calm multi-turn follow-ups.
		if nextIdx > 0 {
			nextIdx = 0
			nextBy = "adaptive-shiftdown"
			if sig.TrailingToolContinuation {
				note = "shifted down to cheapest tier for clean tool continuation"
			} else {
				note = "shifted down to cheapest tier for routine follow-up turn"
			}
		}
		adaptiveThreads.put(cacheKey, threadAdaptiveState{
			lastTierIdx: nextIdx,
			cleanStreak: prev.cleanStreak + 1,
			escalations: 0,
		}, now)

	case sig.DeepInitialPrompt && nextIdx == 0 && !kaiChose:
		// Smart turn-1 start for genuinely complex architecture/spec prompts;
		// turns 2+ will shift right back down to enso-flash once smooth.
		nextIdx = 1
		nextBy = "adaptive-initial"
		note = "started at deeper tier for complex initial prompt; will shift down on routine follow-ups"
		adaptiveThreads.put(cacheKey, threadAdaptiveState{
			lastTierIdx: nextIdx,
			cleanStreak: 0,
			escalations: 0,
		}, now)

	default:
		adaptiveThreads.put(cacheKey, threadAdaptiveState{
			lastTierIdx: nextIdx,
			cleanStreak: prev.cleanStreak + 1,
			escalations: prev.escalations,
		}, now)
	}

	p.sku = ladder[nextIdx]
	p.by = nextBy
	p.adaptiveNote = note
}
