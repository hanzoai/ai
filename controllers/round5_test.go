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

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// Round 5: a provider type that skipped the reasoning strip, reasoning cut short and
// let go as an answer, held text longer than a reader's line, a flag read by the wrong
// name, a lazy quote lifted, and a strict request served over ZAP that HTTP refuses.

// asDigitalOcean makes the relay's provider a DigitalOcean row: the seeded do-ai
// type, which reaches the upstream through model.LocalModelProvider.QueryText.
func asDigitalOcean(t *testing.T) {
	t.Helper()
	p, err := object.GetProvider("admin/relay-a")
	if err != nil || p == nil {
		t.Fatalf("provider: %v", err)
	}
	// The resolver bootstrap installs: a QueryText provider reads the flag through it.
	model.SetOpenerEatenResolver(func(upstream string) bool { return OpenerEaten("", upstream) })
	t.Cleanup(func() { model.SetOpenerEatenResolver(nil) })
	p.Type = "DigitalOcean"
	if _, err := object.UpdateProvider("admin/relay-a", p); err != nil {
		t.Fatal(err)
	}
}

func TestRound5QueryTextStripsOnADigitalOceanRow(t *testing.T) {
	for name, whole := range eatenForms {
		for _, stream := range []string{"true", "false"} {
			w := eatenWorld(t, answerFrames(false, whole[:10], whole[10:])...)
			asDigitalOcean(t)
			c := as(visit("POST", "/v1/messages"), w.cred)
			c.Fiber().Request().SetBody([]byte(`{"model":"` + thinkSku + `","max_tokens":256,"stream":` + stream + `,"messages":[{"role":"user","content":"hi"}]}`))
			c.AnthropicMessages()
			out := sent(c)
			if strings.Contains(out, "brief") || strings.Contains(out, "think>") || !strings.Contains(out, "Hello!") {
				t.Errorf("%s stream=%s: reasoning reached the caller, or the answer did not:\n%s", name, stream, out)
			}
		}
		w := eatenWorld(t, answerFrames(false, whole[:10], whole[10:])...)
		asDigitalOcean(t)
		out := sent(w.chat(`{"model":"` + thinkSku + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		if strings.Contains(out, "brief") {
			t.Errorf("%s console chat: reasoning leaked:\n%s", name, out)
		}
	}
}

func cutFrames(whole string) []string {
	return []string{chunkFrame(`{"role":"assistant","content":""}`, ""),
		chunkFrame(`{"content":`+mustJSON(whole)+`}`, ""), chunkFrame(`{}`, "length")}
}

func TestRound5CutReasoningIsNotTheAnswer(t *testing.T) {
	for name, whole := range map[string]string{
		"eaten, no opener": "The user wants X. I should be brief and secret plan",
		"eaten, opener":    "<think>The user wants X. I should be brief and secret plan",
	} {
		w := eatenWorld(t, cutFrames(whole)...)
		h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)))
		if h.text != "" || h.finish != "length" {
			t.Errorf("%s chat: text %q finish %q, want nothing and length", name, h.text, h.finish)
		}
		w = eatenWorld(t, cutFrames(whole)...)
		if got := anthropicText(t, w); got != "" {
			t.Errorf("%s messages: text %q, want nothing", name, got)
		}
	}
	// A route not flagged, the opener written: still reasoning, still cut.
	w := thinkWorld(t, cutFrames("<think>secret plan")...)
	h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)))
	if strings.Contains(h.text, "secret") {
		t.Errorf("unflagged stream: cut reasoning reached the caller: %q", h.text)
	}
	cutBody := func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"up-1","object":"chat.completion","created":1,"model":"a-up","choices":[{"index":0,"message":{"role":"assistant","content":"<think>secret plan"},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}
	w2 := newRelayWorld(t, cutBody, never(t))
	thinkRoute(t)
	if out := sent(w2.chat(`{"model":"` + thinkSku + `","messages":[{"role":"user","content":"hi"}]}`)); strings.Contains(out, "secret") {
		t.Errorf("unflagged buffered: cut reasoning reached the caller:\n%s", out)
	}
}

func TestRound5FinishedWithoutATerminatorIsTheAnswer(t *testing.T) {
	w := eatenWorld(t, answerFrames(false, "Just ", "an answer.")...)
	if h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))); h.text != "Just an answer." {
		t.Errorf("a flagged route's turn that stopped with no </think>: text %q, want the whole", h.text)
	}
}

func streamedByHand(answer string) []string {
	fr := []string{chunkFrame(`{"role":"assistant","content":""}`, "")}
	for i := 0; i < len(answer); i += 64 {
		fr = append(fr, chunkFrame(`{"content":`+mustJSON(answer[i:min(i+64, len(answer))])+`}`, ""))
	}
	return append(fr, chunkFrame(`{}`, "stop"))
}

func TestRound5LongHeldAnswerKeepsEveryByte(t *testing.T) {
	answer := strings.Repeat("<b>", 100000)
	for _, choice := range []string{``, `,"tool_choice":{"type":"none"}`} {
		w := eatenWorld(t, streamedByHand(answer)...)
		c := as(visit("POST", "/v1/messages"), w.cred)
		c.Fiber().Request().SetBody([]byte(`{"model":"` + thinkSku + `","max_tokens":256,"stream":true,` + anthropicTool + choice + `,"messages":[{"role":"user","content":"hi"}]}`))
		c.AnthropicMessages()
		var text strings.Builder
		last := ""
		for _, e := range chunks(t, sent(c)) {
			switch last = string(e["type"]); last {
			case `"content_block_delta"`:
				text.WriteString(textOf(e["delta"]))
			}
		}
		if text.String() != answer || last == `"error"` {
			t.Errorf("messages choice=%q: got %d of %d bytes, last event %s", choice, text.Len(), len(answer), last)
		}
	}
	w := eatenWorld(t, streamedByHand(answer)...)
	if h := liftHear(t, sent(w.chat(`{"model":"`+thinkSku+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))); h.text != answer {
		t.Errorf("chat: got %d of %d bytes", len(h.text), len(answer))
	}
}

func TestRound5FlagReadFromTheRouteNotTheRowName(t *testing.T) {
	eatenRoute(t)
	if OpenerEaten("acme-byo", "deepseek-r1") {
		t.Fatal("the premise: a row named for an org is not a pair the config names")
	}
	if !openerEatenOn("eaten-test", &object.Provider{Name: "acme-byo", SubType: "deepseek-r1"}) {
		t.Error("an org's own row serving the flagged route's upstream is unflagged")
	}
	if openerEatenOn("eaten-test", &object.Provider{Name: "acme-byo", SubType: "deepseek-v3"}) {
		t.Error("an upstream the route does not name took the route's flag")
	}
}

func TestRound5StrictOverZapIsRefusedAsHTTPRefusesIt(t *testing.T) {
	url, got := nativeUpstream(t, "application/json", nativeReply)
	p := nativePayer(t, "acme", url)
	big := strings.Replace(maximal, `"max_tokens":   2048`, `"max_tokens":   10000000`, 1)

	c := as(visit("POST", "/v1/messages"), p.credential)
	c.Fiber().Request().Header.Set(strictHeader, "1")
	c.Fiber().Request().SetBody([]byte(big))
	c.AnthropicMessages()
	httpStatus := answered(c)

	for name, body := range map[string]string{
		"ceiling": big,
		"stream":  strings.Replace(maximal, `"stream": false`, `"stream": true`, 1),
	} {
		msg, err := gateway(nil)(context.Background(), "", gatewayCall(t, "/v1/messages",
			map[string]string{"Authorization": p.credential, "X-Hanzo-Strict": "1"}, body))
		if err != nil {
			t.Fatal(err)
		}
		status, out := antDecodeGateway(t, msg)
		if status != http.StatusConflict || !strings.Contains(string(out), "strict_violation") {
			t.Errorf("%s over ZAP: status %d, want 409 strict_violation:\n%s", name, status, out)
		}
		if name == "ceiling" && (!strings.Contains(string(out), "ceiling_lowered") || int(status) != httpStatus) {
			t.Errorf("ceiling over ZAP: %d %s; HTTP answered %d", status, out, httpStatus)
		}
	}
	if n, _, _, _ := got.get(); n != 0 {
		t.Errorf("a refused strict request reached the vendor %d times", n)
	}
}
