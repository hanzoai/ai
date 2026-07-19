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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// judgeResp builds a mock OpenAI chat-completion response whose choices[0] message
// content is the judge model's verdict text — so the LLM HTTP call is fully mocked
// and no test ever touches a real endpoint.
func judgeResp(status int, content string) *http.Response {
	body := fmt.Sprintf(`{"choices":[{"message":{"content":%s}}]}`, strconv.Quote(content))
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// TestShouldJudge pins the synchronous eligibility gate: the judge runs ONLY on
// armed config, organic traffic (not judge/probe self-traffic), a turn that carries
// both a prompt and a response, within the sample fraction, for a CONSENTING org,
// AND — the per-request EU/EEA/UK guard — either a non-EU request or an EU request
// from an org that EXPLICITLY opted in. roll is the caller's rand draw, injected so
// the sample gate is deterministic; consent = trainingOptedIn (opt-out default),
// explicit = trainingExplicitlyEnabled (an explicit "enabled").
func TestShouldJudge(t *testing.T) {
	cfg := &judgeConfig{url: "http://x", model: "m", sample: 0.5}
	cases := []struct {
		name                                 string
		cfg                                  *judgeConfig
		ua, org, reqID, country, prompt, res string
		roll                                 float64
		consent, explicit                    bool
		want                                 bool
	}{
		{"disabled (nil cfg)", nil, "", "o", "r", "", "p", "a", 0.0, true, false, false},
		{"judge UA self-skip", cfg, judgeUserAgent, "o", "r", "", "p", "a", 0.0, true, false, false},
		{"probe UA skip (avoids liveness reward race)", cfg, probeUserAgent, "o", "r", "", "p", "a", 0.0, true, false, false},
		{"empty request id", cfg, "curl/8", "o", "", "", "p", "a", 0.0, true, false, false},
		{"empty prompt", cfg, "curl/8", "o", "r", "", "   ", "a", 0.0, true, false, false},
		{"empty response", cfg, "curl/8", "o", "r", "", "p", "  ", 0.0, true, false, false},
		{"sampled out (roll >= sample)", cfg, "curl/8", "o", "r", "", "p", "a", 0.9, true, false, false},
		{"consent off", cfg, "curl/8", "o", "r", "", "p", "a", 0.0, false, false, false},
		{"eligible (non-EU, opt-out default)", cfg, "curl/8", "o", "r", "US", "p", "a", 0.1, true, false, true},
		{"eligible (no geo header)", cfg, "curl/8", "o", "r", "", "p", "a", 0.1, true, false, true},
		// EU/EEA/UK per-request guard: the opt-out default is NOT enough — explicit only.
		{"EU + unset ⇒ skip", cfg, "curl/8", "o", "r", "DE", "p", "a", 0.1, true, false, false},
		{"EU + explicit-on ⇒ allow", cfg, "curl/8", "o", "r", "DE", "p", "a", 0.1, true, true, true},
		{"EU lowercase still guarded", cfg, "curl/8", "o", "r", "fr", "p", "a", 0.1, true, false, false},
		{"UK treated as EU", cfg, "curl/8", "o", "r", "GB", "p", "a", 0.1, true, false, false},
		{"EEA (Norway) treated as EU", cfg, "curl/8", "o", "r", "NO", "p", "a", 0.1, true, false, false},
		{"EU + explicit but consent off ⇒ skip", cfg, "curl/8", "o", "r", "DE", "p", "a", 0.1, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prevOpt, prevExp := trainingOptedIn, trainingExplicitlyEnabled
			trainingOptedIn = func(string) bool { return tc.consent }
			trainingExplicitlyEnabled = func(string) bool { return tc.explicit }
			defer func() { trainingOptedIn, trainingExplicitlyEnabled = prevOpt, prevExp }()
			if got := shouldJudge(tc.cfg, tc.ua, tc.org, tc.reqID, tc.country, tc.prompt, tc.res, tc.roll); got != tc.want {
				t.Fatalf("shouldJudge = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestParseJudgeScore pins the verdict parser: strict JSON, a fenced or prose-wrapped
// object, and nested/brace-containing fields all yield the score; anything else — no
// JSON, malformed, or a score outside [0,1] — is REJECTED so the caller records
// nothing (never a fabricated reward).
func TestParseJudgeScore(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want float64
		ok   bool
	}{
		{"plain", `{"score":0.8,"reason":"ok"}`, 0.8, true},
		{"score only", `{"score":1}`, 1, true},
		{"zero", `{"score":0}`, 0, true},
		{"json fenced", "```json\n{\"score\":0.5,\"reason\":\"meh\"}\n```", 0.5, true},
		{"prose wrapped", `Verdict: {"score":0.42} — done.`, 0.42, true},
		{"reason contains braces", `{"reason":"use {x} not }y{","score":0.6}`, 0.6, true},
		{"nested object before score", `{"meta":{"k":1},"score":0.3}`, 0.3, true},
		{"no json", `the score is 0.9`, 0, false},
		{"malformed", `{"score":}`, 0, false},
		{"above range", `{"score":1.5}`, 0, false},
		{"below range", `{"score":-0.2}`, 0, false},
		{"empty", ``, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseJudgeScore(tc.in)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("parseJudgeScore(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestRunJudgeRewardPath proves a clean verdict routes the score through the SAME
// path as human feedback — attachRoutingReward (the ledger label, "source=judge")
// then forwardRewardToEngine (the online update) — keyed by the request id, with no
// second reward path invented.
func TestRunJudgeRewardPath(t *testing.T) {
	prevDo, prevAttach, prevFwd := judgeDo, attachRoutingReward, forwardRewardToEngine
	defer func() { judgeDo, attachRoutingReward, forwardRewardToEngine = prevDo, prevAttach, prevFwd }()

	var gotOrg, gotReq string
	var gotReward float64
	var attachCalls, fwdCalls int
	attachRoutingReward = func(org, req string, r float64) (bool, error) {
		attachCalls++
		gotOrg, gotReq, gotReward = org, req, r
		return true, nil
	}
	forwardRewardToEngine = func(org, req string, r float64) { fwdCalls++ }
	judgeDo = func(*http.Request) (*http.Response, error) {
		return judgeResp(200, `{"score":0.75,"reason":"good answer"}`), nil
	}

	cfg := &judgeConfig{url: "http://x", model: "judge", sample: 1}
	runJudge(cfg, "org1", "req1", "zen-x", "code", "the prompt", "the response")

	if attachCalls != 1 || gotOrg != "org1" || gotReq != "req1" || gotReward != 0.75 {
		t.Fatalf("attach: calls=%d org=%q req=%q reward=%v; want 1/org1/req1/0.75", attachCalls, gotOrg, gotReq, gotReward)
	}
	if fwdCalls != 1 {
		t.Fatalf("forwardRewardToEngine calls=%d, want 1", fwdCalls)
	}
}

// TestRunJudgeRecordsNothingOnFailure proves that on ANY judge failure — transport
// error, non-2xx, unparseable verdict, out-of-range score, or empty choices — NO
// reward is written and NO engine update fires. A missing reward beats a fake one.
func TestRunJudgeRecordsNothingOnFailure(t *testing.T) {
	prevDo, prevAttach, prevFwd := judgeDo, attachRoutingReward, forwardRewardToEngine
	defer func() { judgeDo, attachRoutingReward, forwardRewardToEngine = prevDo, prevAttach, prevFwd }()

	var attachCalls, fwdCalls int
	attachRoutingReward = func(string, string, float64) (bool, error) { attachCalls++; return true, nil }
	forwardRewardToEngine = func(string, string, float64) { fwdCalls++ }
	cfg := &judgeConfig{url: "http://x", model: "judge", sample: 1}

	cases := []struct {
		name string
		do   func(*http.Request) (*http.Response, error)
	}{
		{"transport error", func(*http.Request) (*http.Response, error) { return nil, errors.New("dial fail") }},
		{"non-2xx", func(*http.Request) (*http.Response, error) { return judgeResp(500, `{"score":1}`), nil }},
		{"unparseable verdict", func(*http.Request) (*http.Response, error) { return judgeResp(200, `not json at all`), nil }},
		{"out-of-range score", func(*http.Request) (*http.Response, error) { return judgeResp(200, `{"score":9}`), nil }},
		{"empty choices", func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Header: make(http.Header)}, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attachCalls, fwdCalls = 0, 0
			judgeDo = tc.do
			runJudge(cfg, "o", "r", "m", "t", "p", "resp")
			if attachCalls != 0 || fwdCalls != 0 {
				t.Fatalf("recorded on failure: attach=%d fwd=%d, want 0/0", attachCalls, fwdCalls)
			}
		})
	}
}

// TestRunJudgeUnknownRequestSkipsForward proves that when the request id matches no
// event (found=false — unknown or cross-org), the engine update is NOT forwarded:
// the reward is anonymous and org-scoped, so a misjoin records nothing downstream.
func TestRunJudgeUnknownRequestSkipsForward(t *testing.T) {
	prevDo, prevAttach, prevFwd := judgeDo, attachRoutingReward, forwardRewardToEngine
	defer func() { judgeDo, attachRoutingReward, forwardRewardToEngine = prevDo, prevAttach, prevFwd }()

	var fwdCalls int
	attachRoutingReward = func(string, string, float64) (bool, error) { return false, nil }
	forwardRewardToEngine = func(string, string, float64) { fwdCalls++ }
	judgeDo = func(*http.Request) (*http.Response, error) { return judgeResp(200, `{"score":0.5}`), nil }

	cfg := &judgeConfig{url: "http://x", model: "judge", sample: 1}
	runJudge(cfg, "o", "unknown-req", "m", "t", "p", "resp")
	if fwdCalls != 0 {
		t.Fatalf("forwarded for unknown request id: calls=%d, want 0", fwdCalls)
	}
}

// TestJudgeRubricShape checks the rubric carries the task, prompt, and response in
// two messages, and omits the task line when the task is unknown.
func TestJudgeRubricShape(t *testing.T) {
	msgs := judgeRubric("code", "MY_PROMPT", "MY_RESPONSE")
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[1]["role"] != "user" {
		t.Fatalf("unexpected rubric shape: %+v", msgs)
	}
	u := msgs[1]["content"]
	if !strings.Contains(u, "TASK TYPE: code") || !strings.Contains(u, "MY_PROMPT") || !strings.Contains(u, "MY_RESPONSE") {
		t.Fatalf("rubric missing fields: %q", u)
	}
	if strings.Contains(judgeRubric("", "P", "R")[1]["content"], "TASK TYPE") {
		t.Fatal("task line present for empty task")
	}
}

// TestRefreshJudgeConfigDynamic proves the judge config is DB-backed and DYNAMIC —
// no env. refreshJudgeConfig folds whatever loadJudgeConfig (the "*" OrgSettings row
// in production) returns into the hot-path atomic: an enabled config arms the panel
// (self URL + default sample filled in, explicit URL/sample flowing through
// verbatim); flipping to disabled disarms it (nil ⇒ the post-response hook is a pure
// no-op) — the live admin.hanzo.ai toggle, no restart.
func TestRefreshJudgeConfigDynamic(t *testing.T) {
	prevLoad, prevCfg, prevSig := loadJudgeConfig, judgeCfg.Load(), lastJudgeSig
	defer func() {
		loadJudgeConfig, lastJudgeSig = prevLoad, prevSig
		judgeCfg.Store(prevCfg)
	}()

	// Enabled, 2-model panel, no URL/sample → armed with self URL + default sample.
	loadJudgeConfig = func() object.JudgeConfig { return object.JudgeConfig{Enabled: true, Models: "a,b"} }
	refreshJudgeConfig()
	got := judgeCfg.Load()
	if got == nil {
		t.Fatal("enabled config did not arm the judge")
	}
	if len(got.models) != 2 || got.url != judgeSelfURL || got.sample != object.JudgeSampleDefault {
		t.Fatalf("armed cfg = %+v, want 2 models / self URL / default sample", got)
	}

	// Explicit URL + sample flow through verbatim (trailing slash trimmed).
	loadJudgeConfig = func() object.JudgeConfig {
		return object.JudgeConfig{Enabled: true, Models: "solo", URL: "https://tee.example/", Sample: 0.42}
	}
	refreshJudgeConfig()
	got = judgeCfg.Load()
	if got == nil || got.url != "https://tee.example" || got.sample != 0.42 || len(got.models) != 1 {
		t.Fatalf("armed cfg = %+v, want solo / trimmed URL / 0.42", got)
	}

	// Disabled → disarmed (nil): judgeRoutedResponse becomes a single-atomic-load no-op.
	loadJudgeConfig = func() object.JudgeConfig { return object.JudgeConfig{Enabled: false} }
	refreshJudgeConfig()
	if judgeCfg.Load() != nil {
		t.Fatal("disabled config did not disarm the judge")
	}
}

// TestJudgeDefaultEnabledPanel proves the deploy-time default: the built-in judge
// config (what object.GetCachedJudgeConfig returns with no "*" row) is ENABLED and
// resolves to a DIVERSE Mean-Field Judge Panel of >1 served model at ~10% sample —
// the judge runs the panel the moment the binary boots, before any admin action.
func TestJudgeDefaultEnabledPanel(t *testing.T) {
	prevLoad, prevCfg, prevSig := loadJudgeConfig, judgeCfg.Load(), lastJudgeSig
	defer func() {
		loadJudgeConfig, lastJudgeSig = prevLoad, prevSig
		judgeCfg.Store(prevCfg)
	}()

	loadJudgeConfig = func() object.JudgeConfig {
		return object.JudgeConfig{Enabled: object.JudgeEnabledDefault, Models: object.JudgeModelsDefault, Sample: object.JudgeSampleDefault}
	}
	refreshJudgeConfig()
	got := judgeCfg.Load()
	if got == nil {
		t.Fatal("default config is not enabled — the judge must be ON by default")
	}
	if len(got.models) < 2 {
		t.Fatalf("default panel size = %d, want >=2 (a diverse MFJP)", len(got.models))
	}
	if got.sample <= 0 || got.sample > 1 {
		t.Fatalf("default sample = %v, want in (0,1]", got.sample)
	}
}

// TestIsEURequest pins the geo classifier the per-request GDPR guard keys off:
// EU-27 + EEA + UK are EU (case-insensitive); a non-EU country and an empty (no
// edge geo) header are NOT — the guard fires only on a positive EU signal.
func TestIsEURequest(t *testing.T) {
	for _, c := range []string{"DE", "fr", " gb ", "NO", "IS", "LI", "IE"} {
		if !isEURequest(c) {
			t.Errorf("isEURequest(%q) = false, want true", c)
		}
	}
	for _, c := range []string{"US", "CA", "JP", "", "  ", "ZZ", "CH"} {
		if isEURequest(c) {
			t.Errorf("isEURequest(%q) = true, want false", c)
		}
	}
}

// TestTruncateForJudge bounds input cost: long content is clipped and marked, short
// content is passed through unchanged.
func TestTruncateForJudge(t *testing.T) {
	if got := truncateForJudge("short"); got != "short" {
		t.Fatalf("short mangled: %q", got)
	}
	long := strings.Repeat("x", judgeMaxChars+50)
	got := truncateForJudge(long)
	if !strings.HasSuffix(got, "[truncated]") {
		t.Fatal("truncation marker missing")
	}
	if len([]rune(got)) >= len([]rune(long)) {
		t.Fatal("content not truncated")
	}
}
