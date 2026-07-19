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
	"bytes"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/ai/log"
)

// ── Router judge — LLM-as-a-judge dense quality rewards ───────────────────────
//
// The enso flywheel learns from the REWARD attached to each contentless
// RoutingEvent. Until now the only AUTOMATED reward was the self-probe's liveness
// tick (2xx + non-empty ⇒ 1.0) — it says the route WORKED, not that the answer was
// GOOD. This closes that gap: after a chat turn is served, a small/cheap judge
// model scores the (prompt, response) pair on a 0..1 quality scale, and that scalar
// flows through the SAME reward path as human feedback (attachRoutingReward +
// forwardRewardToEngine). The router then prefers models that answer WELL, not just
// models that answer at all.
//
// PRIVACY — anonymous, content-transient, consent-gated (enforced in code here):
//
//   - Anonymous: the reward is keyed ONLY by the request id → the contentless
//     RoutingEvent. No user id, email, org PII, prompt, or response text is EVER
//     persisted with the reward. The ledger row gains exactly one number (reward)
//     and a timestamp (rewarded_time) — identical to a human up-vote.
//   - Content-transient: the prompt + response are seen ONLY in-process, held only
//     for the duration of the judge call, and then discarded. runJudge emits a
//     scalar; the judge's free-text "reason" is parsed but DELIBERATELY DROPPED
//     (parseJudgeScore never returns it) because it can paraphrase content and
//     nothing downstream stores it.
//   - Consent-gated: only orgs where trainingOptedIn(org) is true are judged. An
//     opted-out org's turns are never sent to the judge at all (shouldJudge).
//
// CONFIDENTIAL COMPUTE — the endpoint is the seam (be honest about the split):
//
//	ROUTER_JUDGE_URL points the judge inference at ANY OpenAI-compatible endpoint.
//	Point it at an ATTESTED TEE / enclave inference service (Hanzo confidential
//	compute) and the prompt+response are processed inside the enclave, never visible
//	to operators — that is what buys TRUE confidentiality of the in-flight content.
//	This FILE cannot provide enclave isolation; what the CODE guarantees regardless
//	of endpoint is no-content-retention: nothing here writes prompt/response/reason
//	to disk, DB, log, or the reward row. The two are complementary — code = "never
//	stored", TEE endpoint = "never seen". Default is self (http://127.0.0.1:8000):
//	no confidentiality boundary, only the no-retention guarantee.
//
// OFF unless configured (mirrors the probe/trainer; all three required):
//
//	ROUTER_JUDGE_ENABLED — "1"/"true" to run (default off)
//	ROUTER_JUDGE_MODEL   — the judge model id (a small/cheap model; required)
//	ROUTER_JUDGE_TOKEN   — the service bearer the judge call presents (required)
//	ROUTER_JUDGE_URL     — judge endpoint; default http://127.0.0.1:8000 (self).
//	                       Set to an attested TEE endpoint for confidential inference.
//	ROUTER_JUDGE_SAMPLE  — fraction of eligible turns to judge, 0<f<=1 (default 0.1)
//
// REWARD SOURCE — noted, not a column. A judge reward is INDISTINGUISHABLE at the
// row level from a human reward: the schema stores only {reward, rewarded_time}
// (object.RoutingEvent), which IS the anonymity property — the ledger cannot tell
// who or what scored a request. router/stats' engine_share is the routing-decision
// origin (engine|heuristic), not the reward origin, and reward_rate counts a judge
// reward exactly like a human one. Operationally the three automated/human sources
// separate by seam: the probe posts liveness over /v1/add-routing-reward under a
// service key + User-Agent hanzo-router-probe/1; the judge scores organic turns
// in-process (this file) for consenting orgs at the sample rate; humans POST
// /v1/feedback. If a future schema adds a reward_source column, runJudge is the one
// place to stamp "judge" — a single line — without touching the reward path.
const judgeUserAgent = "hanzo-router-judge/1"

// judgeMaxChars caps how much of the prompt/response is sent to the judge — bounds
// input cost on long turns; a quality verdict does not need the whole transcript.
const judgeMaxChars = 6000

// judgeConfig is the resolved, immutable judge configuration built once at boot.
type judgeConfig struct {
	url    string
	model  string
	token  string
	sample float64 // (0,1] — fraction of eligible turns judged
}

// judgeCfg holds the active config, or nil when the judge is disabled. Written once
// at boot (StartRouterJudge, before the server serves) and read on every served
// chat turn — atomic.Pointer so the hot-path read is race-free AND near-free (a
// single pointer load) on the dominant path where the judge is off.
var judgeCfg atomic.Pointer[judgeConfig]

// judgeClient is the bounded HTTP client for judge calls (short timeout: a slow
// judge must never pile up goroutines behind live traffic).
var judgeClient = &http.Client{Timeout: 30 * time.Second}

// judgeDo performs the judge HTTP call. Indirected through a var so tests mock the
// judge model without a live endpoint; production uses judgeClient.
var judgeDo = func(req *http.Request) (*http.Response, error) { return judgeClient.Do(req) }

// judgeVerdict is the strict JSON the judge model is asked to return. Score is the
// only field used; Reason is parsed to validate the shape but never returned or
// stored (it can paraphrase content — see parseJudgeScore).
type judgeVerdict struct {
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

// StartRouterJudge arms the LLM-as-a-judge reward path when configured. Called once
// at boot (Bootstrap, next to StartRouterProbe/StartRouterTrainer). Never fails
// boot: misconfiguration logs and stays disabled (judgeCfg nil ⇒ the hook is a
// no-op). Self-gating and OFF by default — dark until deployment config turns it on.
func StartRouterJudge() {
	if !envTrue("ROUTER_JUDGE_ENABLED") {
		log.Info("router judge: disabled (set ROUTER_JUDGE_ENABLED=1 to enable)")
		return
	}
	model := strings.TrimSpace(os.Getenv("ROUTER_JUDGE_MODEL"))
	token := strings.TrimSpace(os.Getenv("ROUTER_JUDGE_TOKEN"))
	if model == "" || token == "" {
		log.Info("router judge: disabled (ROUTER_JUDGE_MODEL + ROUTER_JUDGE_TOKEN required)")
		return
	}
	url := strings.TrimRight(strings.TrimSpace(os.Getenv("ROUTER_JUDGE_URL")), "/")
	if url == "" {
		url = "http://127.0.0.1:8000"
	}
	sample := envFloat("ROUTER_JUDGE_SAMPLE", 0.1)
	if sample <= 0 || sample > 1 {
		sample = 0.1
	}
	judgeCfg.Store(&judgeConfig{url: url, model: model, token: token, sample: sample})
	log.Info("router judge: enabled — model %s, sample %.2f, endpoint %s (point ROUTER_JUDGE_URL at an attested TEE endpoint for confidential inference)", model, sample, url)
}

// judgeRoutedResponse is the fire-and-forget hook invoked AFTER a chat response has
// been fully served (openai_api.go, the common tail of the stream/non-stream split).
// It NEVER blocks or delays the user response: the caller has already written the
// reply; this does cheap synchronous gates (shouldJudge) and, only if they pass,
// spawns ONE goroutine for the judge call + reward write. On the dominant path
// (judge off) it costs a single atomic load and returns.
func judgeRoutedResponse(userAgent, org, requestId, model, task, prompt, response string) {
	cfg := judgeCfg.Load()
	if !shouldJudge(cfg, userAgent, org, requestId, prompt, response, rand.Float64()) {
		return
	}
	go runJudge(cfg, org, requestId, model, task, prompt, response)
}

// shouldJudge is the synchronous, side-effect-free eligibility gate — split out so
// the whole decision is unit-testable without spawning the async call (roll is the
// caller's rand draw). A turn is judged only when: the judge is armed (cfg != nil),
// it is ORGANIC traffic (not the judge's own calls nor the self-probe — judging
// either would recurse or race the probe's liveness reward on the same request id),
// it actually carries a prompt AND a response, it falls within the sample fraction,
// and the org has CONSENTED to contribute training labels (trainingOptedIn).
func shouldJudge(cfg *judgeConfig, userAgent, org, requestId, prompt, response string, roll float64) bool {
	if cfg == nil {
		return false
	}
	if userAgent == judgeUserAgent || userAgent == probeUserAgent {
		return false
	}
	if requestId == "" || strings.TrimSpace(prompt) == "" || strings.TrimSpace(response) == "" {
		return false
	}
	if roll >= cfg.sample {
		return false
	}
	return trainingOptedIn(org)
}

// runJudge calls the judge model on the (prompt, response) pair and, ONLY on a clean
// verdict, routes the score through the SAME reward path as human feedback —
// attachRoutingReward (the ledger label) then forwardRewardToEngine (the online
// LinUCB update). On ANY failure (HTTP error, non-2xx, unparseable, out-of-range
// score, or an unknown/cross-org request id) it records NOTHING: a fabricated or
// misjoined reward poisons training worse than a missing one. Content lives only in
// this call's arguments and is gone when it returns.
func runJudge(cfg *judgeConfig, org, requestId, model, task, prompt, response string) {
	score, ok := judgeScore(cfg, task, prompt, response)
	if !ok {
		return
	}
	found, err := attachRoutingReward(org, requestId, score)
	if err != nil || !found {
		return
	}
	forwardRewardToEngine(org, requestId, score)
	// Anonymous telemetry: the join key + the scalar + the arm — never content.
	log.Info("router judge: scored %s → reward %.2f (model %s, task %s)", requestId, score, model, task)
}

// judgeScore performs the judge completion and returns the parsed score. Bounded by
// construction: small max_tokens, temperature 0, an X-Max-Cost ceiling, and a
// capped response read. Returns ok=false on any transport/shape failure.
func judgeScore(cfg *judgeConfig, task, prompt, response string) (float64, bool) {
	body, _ := json.Marshal(map[string]any{
		"model":       cfg.model,
		"max_tokens":  256,
		"temperature": 0,
		"messages":    judgeRubric(task, prompt, response),
	})
	req, err := http.NewRequest(http.MethodPost, cfg.url+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, false
	}
	req.Header.Set("Authorization", "Bearer "+cfg.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", judgeUserAgent)
	req.Header.Set("X-Max-Cost", "0.02")

	resp, err := judgeDo(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, false
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &out) != nil || len(out.Choices) == 0 {
		return 0, false
	}
	return parseJudgeScore(out.Choices[0].Message.Content)
}

// judgeRubric builds the judge messages. Pure in (task, prompt, response) so the
// prompt shape is unit-testable. Prompt + response are truncated to bound input
// cost; content lives ONLY in these in-memory strings — sent to the judge endpoint,
// never persisted.
func judgeRubric(task, prompt, response string) []map[string]string {
	sys := "You are a strict, impartial evaluator of AI assistant responses. " +
		"Score how well the RESPONSE answers the USER PROMPT on a 0.0 to 1.0 scale: " +
		"1.0 = fully correct, complete, and helpful; 0.5 = partially useful; " +
		"0.0 = wrong, empty, or an unwarranted refusal. Weigh correctness, " +
		"completeness, and helpfulness, and penalize refusals when the prompt is " +
		"answerable. Reply with STRICT JSON only and nothing else: " +
		`{"score": <number 0..1>, "reason": "<one short sentence>"}.`
	taskLine := ""
	if strings.TrimSpace(task) != "" {
		taskLine = "TASK TYPE: " + task + "\n\n"
	}
	user := taskLine +
		"USER PROMPT:\n" + truncateForJudge(prompt) + "\n\n" +
		"RESPONSE:\n" + truncateForJudge(response) + "\n\n" +
		"Return the JSON verdict."
	return []map[string]string{
		{"role": "system", "content": sys},
		{"role": "user", "content": user},
	}
}

// truncateForJudge caps a string to judgeMaxChars runes, marking the cut so the
// judge knows the transcript was clipped.
func truncateForJudge(s string) string {
	r := []rune(s)
	if len(r) <= judgeMaxChars {
		return s
	}
	return string(r[:judgeMaxChars]) + "\n…[truncated]"
}

// parseJudgeScore extracts the score from the judge's reply. The model is asked for
// strict JSON but may wrap it in prose or a ```json fence, so this reads the FIRST
// brace-balanced {...} object and its score. Returns ok=false on any deviation (no
// JSON, unparseable, or score outside [0,1]) — the caller then records NOTHING. The
// verdict's free-text reason is intentionally NOT returned: it can paraphrase
// content, and nothing downstream stores it, so it never leaves this function.
func parseJudgeScore(content string) (float64, bool) {
	obj := firstJSONObject(content)
	if obj == "" {
		return 0, false
	}
	var v judgeVerdict
	if json.Unmarshal([]byte(obj), &v) != nil {
		return 0, false
	}
	if v.Score < 0 || v.Score > 1 {
		return 0, false
	}
	return v.Score, true
}

// firstJSONObject returns the first brace-balanced {...} substring of s (so a
// verdict wrapped in prose or a code fence still parses), or "" when there is none.
// Brace counting ignores braces inside JSON strings and honors backslash escapes.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case ch == '\\':
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
