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
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
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
//   - Consent-gated: only orgs where trainingOptedIn(org) is true are judged (the
//     global opt-out default; an explicit "disabled" drops them), AND a per-request
//     EU/EEA/UK guard requires EXPLICIT opt-in before judging a turn geolocated to
//     those jurisdictions (shouldJudge + isEURequest). An opted-out org's turns —
//     and any EU turn from a non-explicitly-consenting org — are never judged.
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
// DYNAMIC config — DB-backed, live-tunable at admin.hanzo.ai, no env, no restart.
// The knobs live on the "*" GlobalDefaultOwner OrgSettings row (the SAME settings
// system the trainer's "*" RouterPrefer uses), resolved by object.GetCachedJudgeConfig
// and refreshed on a ticker (refreshJudgeConfig):
//
//	judge.enabled (JudgeEnabled) — three-state; unset ⇒ the built-in default ON
//	judge.models  (JudgeModels)  — comma list of served ids; >1 ⇒ the MFJP panel
//	judge.url     (JudgeURL)     — endpoint; "" ⇒ self; a TEE endpoint ⇒ confidential
//	judge.sample  (JudgeSample)  — fraction of eligible turns judged, 0<f<=1
//
// ON by default: the built-in defaults run the diverse panel on ~10% of eligible
// traffic the moment the binary boots; an admin disables or retunes it live. The
// judge presents NO bespoke token — it authenticates its self-directed chat call
// with the gateway's EXISTING internal service bearer (internalServiceToken, the
// same ROUTER_PROBE_TOKEN the self-probe uses); the secret never enters the DB.
//
// REWARD SOURCE — noted, not a column. A judge reward is INDISTINGUISHABLE at the
// row level from a human reward: the schema stores only {reward, rewarded_time}
// (object.RoutingEvent), which IS the anonymity property — the ledger cannot tell
// who or what scored a request. router/stats' by_source is the routing-decision
// origin (heuristic|explore|override|…), not the reward origin, and reward_rate
// counts a judge reward exactly like a human one. The three automated/human sources
// separate by seam: the probe posts liveness over /v1/feedback under a
// service key + User-Agent hanzo-router-probe/1; the judge scores organic turns
// in-process (this file) for consenting orgs at the sample rate; humans POST
// /v1/feedback. If a future schema adds a reward_source column, runJudge is the one
// place to stamp "judge" — a single line — without touching the reward path.
const judgeUserAgent = "hanzo-router-judge/1"

// judgeMaxChars caps how much of the prompt/response is sent to the judge — bounds
// input cost on long turns; a quality verdict does not need the whole transcript.
const judgeMaxChars = 6000

// judgeSelfURL is the default judge endpoint — the gateway's own listen address, so
// the judge scores through the same public path as real traffic. Overridden by the
// dynamic judge.url (point it at an attested TEE endpoint for confidential inference).
const judgeSelfURL = "http://127.0.0.1:8000"

// judgeRefreshEvery is how often the judge re-reads its DB-backed config from the
// "*" GlobalDefaultOwner row — matched to the OrgSettings cache TTL so an
// admin.hanzo.ai change takes effect within ~one window, no restart.
const judgeRefreshEvery = 60 * time.Second

// judgeConfig is the resolved judge configuration held in the hot-path atomic. It
// carries NO secret — the service bearer is resolved at call time
// (internalServiceToken), never stored here or in the DB.
type judgeConfig struct {
	url    string
	model  string   // primary/single judge model (also models[0])
	models []string // the judge PANEL: >1 model ⇒ Mean-Field Judge Panel (MFJP)
	sample float64  // (0,1] — fraction of eligible turns judged
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

// loadJudgeConfig is the resolved DB-backed judge config source — indirected
// through a var so tests drive refreshJudgeConfig without a live settings row.
// Production reads the "*" GlobalDefaultOwner OrgSettings row via the 60s cache.
var loadJudgeConfig = object.GetCachedJudgeConfig

// StartRouterJudge arms the LLM-as-a-judge reward path from DYNAMIC, DB-backed
// config — no env, no restart. Called once at boot (Bootstrap, next to
// StartRouterProbe/StartRouterTrainer). It loads the config ONCE synchronously (so
// the judge is armed before the server serves) then refreshes it on a ticker;
// admin.hanzo.ai edits to the "*" row take effect within one refresh window. ON by
// default (object.GetCachedJudgeConfig's built-in defaults run the diverse panel on
// ~10% of eligible traffic); an admin disables or retunes it live. Never fails boot.
func StartRouterJudge() {
	refreshJudgeConfig()
	go func() {
		t := time.NewTicker(judgeRefreshEvery)
		defer t.Stop()
		for range t.C {
			refreshJudgeConfig()
		}
	}()
}

// refreshJudgeConfig folds the live DB config into the hot-path atomic judgeCfg:
// nil when disabled (the post-response hook is then a single atomic-load no-op),
// else the resolved panel with the self endpoint + default sample filled in. Pure
// of secrets — the bearer is resolved separately at call time. Logs only on a
// change (logJudgeConfig), so the 60s tick is silent in steady state.
func refreshJudgeConfig() {
	c := loadJudgeConfig()
	var next *judgeConfig
	if c.Enabled {
		url := strings.TrimRight(strings.TrimSpace(c.URL), "/")
		if url == "" {
			url = judgeSelfURL
		}
		sample := c.Sample
		if sample <= 0 || sample > 1 {
			sample = object.JudgeSampleDefault
		}
		models := splitModels(c.Models)
		next = &judgeConfig{url: url, model: models[0], models: models, sample: sample}
	}
	judgeCfg.Store(next)
	logJudgeConfig(next)
}

// lastJudgeSig is the last-logged config signature — touched only by the boot call
// and then the single refresh goroutine (never concurrently), so a plain string is
// race-free. Empty means "nothing logged yet".
var lastJudgeSig string

// logJudgeConfig logs the judge posture ONLY when it changes from the last tick, so
// the periodic refresh does not spam the log. A nil config logs the disabled line.
func logJudgeConfig(cfg *judgeConfig) {
	// The credential is part of the posture: an armed judge with no token scores
	// NOTHING, so it belongs in the signature (the line re-fires when one appears).
	armed := internalServiceToken() != ""
	sig := "disabled"
	if cfg != nil {
		sig = fmt.Sprintf("%s@%s×%.2f/%t", strings.Join(cfg.models, ","), cfg.url, cfg.sample, armed)
	}
	if sig == lastJudgeSig {
		return
	}
	lastJudgeSig = sig
	switch {
	case cfg == nil:
		log.Info("router judge: disabled (set the \"*\" OrgSettings judgeEnabled=disabled row at admin.hanzo.ai to keep it off; unset ⇒ ON)")
	case len(cfg.models) > 1:
		log.Info("router judge: enabled — MEAN-FIELD PANEL of %d judges %v, sample %.2f, endpoint %s", len(cfg.models), cfg.models, cfg.sample, cfg.url)
	default:
		log.Info("router judge: enabled — model %s, sample %.2f, endpoint %s (set judge.url at admin.hanzo.ai to an attested TEE endpoint for confidential inference)", cfg.models[0], cfg.sample, cfg.url)
	}
	// An enabled judge without the service bearer is the silent-starvation case: every
	// call answers 401, every score abstains, the reward ledger stays empty and the
	// trainer's gate never clears. Say so ONCE, loudly, at the point of arming.
	if cfg != nil && !armed {
		log.Warning("router judge: NO internal service token — set ROUTER_PROBE_TOKEN (env or KMS-synced conf) or every judge call to %s answers 401, no reward is ever recorded, and the trainer cannot clear its gate", cfg.url)
	}
}

// internalServiceToken returns the gateway's EXISTING internal service bearer — the
// SAME credential the self-probe presents (ROUTER_PROBE_TOKEN, env then KMS-synced
// conf). The judge is another internal self-caller hitting /v1/chat/completions on
// the internal org's dime, so it REUSES that one service credential rather than
// inventing a second token: no bespoke ROUTER_JUDGE_TOKEN. The secret NEVER enters
// the DB config — it is sourced only here, from env/KMS.
func internalServiceToken() string {
	if t := strings.TrimSpace(os.Getenv("ROUTER_PROBE_TOKEN")); t != "" {
		return t
	}
	return strings.TrimSpace(conf.GetConfigString("ROUTER_PROBE_TOKEN"))
}

// judgeRoutedResponse is the fire-and-forget hook invoked AFTER a chat response has
// been fully served (openai_api.go, the common tail of the stream/non-stream split).
// It NEVER blocks or delays the user response: the caller has already written the
// reply; this does cheap synchronous gates (shouldJudge) and, only if they pass,
// spawns ONE goroutine for the judge call + reward write. On the dominant path
// (judge off) it costs a single atomic load and returns.
func judgeRoutedResponse(userAgent, org, requestId, country, model, task, prompt, response string) {
	cfg := judgeCfg.Load()
	if !shouldJudge(cfg, userAgent, org, requestId, country, prompt, response, rand.Float64()) {
		return
	}
	go runJudge(cfg, org, requestId, model, task, prompt, response)
}

// euCountries is the EU + EEA + UK set for the per-request training-data guard. In
// these jurisdictions the opt-out default is NOT sufficient consent to process a
// user's content for training — the org must EXPLICITLY opt in. Keyed by the
// edge-supplied CF-IPCountry (ISO-3166-1 alpha-2), so the protection follows the
// REQUEST's origin regardless of the org's home region.
var euCountries = map[string]bool{
	// EU-27
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true,
	"DK": true, "EE": true, "FI": true, "FR": true, "DE": true, "GR": true,
	"HU": true, "IE": true, "IT": true, "LV": true, "LT": true, "LU": true,
	"MT": true, "NL": true, "PL": true, "PT": true, "RO": true, "SK": true,
	"SI": true, "ES": true, "SE": true,
	// EEA (non-EU) + UK
	"IS": true, "LI": true, "NO": true, "GB": true,
}

// isEURequest reports whether the edge-tagged country is in the EU/EEA/UK set.
// Case-insensitive; an empty country (no edge geo) is treated as NON-EU — the guard
// fires only on a POSITIVE EU signal, so it never blocks traffic we cannot geolocate.
func isEURequest(country string) bool {
	return euCountries[strings.ToUpper(strings.TrimSpace(country))]
}

// shouldJudge is the synchronous, side-effect-free eligibility gate — split out so
// the whole decision is unit-testable without spawning the async call (roll is the
// caller's rand draw). A turn is judged only when: the judge is armed (cfg != nil),
// it is ORGANIC traffic (not the judge's own calls nor the self-probe — judging
// either would recurse or race the probe's liveness reward on the same request id),
// it actually carries a prompt AND a response, it falls within the sample fraction,
// the org has CONSENTED to contribute training labels (trainingOptedIn — the global
// opt-out default), AND — the load-bearing GDPR/UK-GDPR protection — if the request
// is geolocated to the EU/EEA/UK, the org has EXPLICITLY opted in
// (trainingExplicitlyEnabled). Explicit consent always wins; the opt-out default is
// NOT enough for an EU-tagged request.
func shouldJudge(cfg *judgeConfig, userAgent, org, requestId, country, prompt, response string, roll float64) bool {
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
	if !trainingOptedIn(org) {
		return false
	}
	if isEURequest(country) && !trainingExplicitlyEnabled(org) {
		return false
	}
	return true
}

// runJudge calls the judge model on the (prompt, response) pair and, ONLY on a clean
// verdict, routes the score through the SAME reward path as human feedback —
// attachRoutingReward (the ledger label) then forwardRewardToEngine (the online
// LinUCB update). On ANY failure (HTTP error, non-2xx, unparseable, out-of-range
// score, or an unknown/cross-org request id) it records NOTHING: a fabricated or
// misjoined reward poisons training worse than a missing one. Content lives only in
// this call's arguments and is gone when it returns.
func runJudge(cfg *judgeConfig, org, requestId, model, task, prompt, response string) {
	var score float64
	var ok bool
	if len(cfg.models) > 1 {
		// Diverse panel: calibrated, reliability-weighted consensus (see MFJP).
		score, _, ok = panelScore(cfg, cfg.models, task, prompt, response)
	} else {
		score, ok = judgeScore(cfg, task, prompt, response)
	}
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

// judgeMissLogEvery bounds how often a failing judge writes a line — a judge that
// 401s on every call must be VISIBLE without one line per scored turn.
const judgeMissLogEvery = time.Minute

// judgeMisses counts judge scoring calls that failed since boot; lastJudgeMissLog
// is the unix-nano stamp of the last line written.
var (
	judgeMisses      atomic.Uint64
	lastJudgeMissLog atomic.Int64
)

// JudgeMisses is the number of judge scoring calls that failed since boot. Read it
// beside rewarded_events: a count that climbs while rewards stay flat means the
// judge is BROKEN, not merely sampling — the signal that a silently abstaining
// judge starved the trainer of the rewards its gate needs.
func JudgeMisses() uint64 { return judgeMisses.Load() }

// judgeMiss records a failed judge call and returns the abstain result, so every
// failure path reads `return judgeMiss(...)`. reason is always CONTENT-FREE (status
// codes and transport errors only) — the judge's no-retention guarantee extends to
// its own logs. Counting is unconditional; logging is rate-limited.
func judgeMiss(model, reason string) (float64, bool) {
	n := judgeMisses.Add(1)
	now := time.Now().UnixNano()
	if prev := lastJudgeMissLog.Load(); now-prev >= int64(judgeMissLogEvery) && lastJudgeMissLog.CompareAndSwap(prev, now) {
		log.Warning("router judge: %s could not score (%s) — NO reward recorded; %d judge failures since boot", model, reason, n)
	}
	return 0, false
}

// judgeScore performs the judge completion and returns the parsed score. Bounded by
// construction: small max_tokens, temperature 0, an X-Max-Cost ceiling, and a
// capped response read. Returns ok=false on any transport/shape failure, always via
// judgeMiss so the failure is counted and periodically logged.
func judgeScore(cfg *judgeConfig, task, prompt, response string) (float64, bool) {
	body, _ := json.Marshal(map[string]any{
		"model":       cfg.model,
		"max_tokens":  256,
		"temperature": 0,
		"messages":    judgeRubric(task, prompt, response),
	})
	req, err := http.NewRequest(http.MethodPost, cfg.url+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return judgeMiss(cfg.model, "build request: "+err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+internalServiceToken())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", judgeUserAgent)
	req.Header.Set("X-Max-Cost", "0.02")

	resp, err := judgeDo(req)
	if err != nil {
		return judgeMiss(cfg.model, "transport: "+err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := "HTTP " + strconv.Itoa(resp.StatusCode) + " from " + cfg.url
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			reason += " — the internal service token is missing or rejected (ROUTER_PROBE_TOKEN)"
		}
		return judgeMiss(cfg.model, reason)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &out) != nil || len(out.Choices) == 0 {
		return judgeMiss(cfg.model, "reply carried no choices")
	}
	score, ok := parseJudgeScore(out.Choices[0].Message.Content)
	if !ok {
		return judgeMiss(cfg.model, "verdict was not a parseable 0..1 score")
	}
	return score, true
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
