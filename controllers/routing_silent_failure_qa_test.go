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

// routing_silent_failure_qa_test.go — QA bounty #137: the silent failures.
//
// The failover tests elsewhere prove a vendor that SAYS it failed (a status, an error
// frame, an error object) is moved. These are the vendors that fail without saying
// so: a 200 that holds no answer, and a stream that stops before the answer is done.
// Each would reach the caller as a success.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

const plainAsk = `{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`
const streamAsk = `{"model":"relay-sku","stream":true,"messages":[{"role":"user","content":"hi"}]}`

// cutTold is whether a caller's stream ends with the error frame that says the
// answer did not finish.
func cutTold(out string) bool {
	return strings.Contains(out, `"type":"upstream_error"`) && strings.Contains(out, "before the answer finished")
}

func chunkOf(content string) string {
	return fmt.Sprintf(`data: {"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[{"index":0,"delta":{"content":%q}}]}`+"\n\n", content)
}

// ── streams that stop early ─────────────────────────────────────────────────

// A vendor whose connection drops mid-answer. The caller keeps what it was sent and
// the request stays where it began (TestAStreamNeverMovesAfterItsFirstByte), but the
// stream must not end as if the answer were whole: no [DONE], and an error frame an
// SDK raises on.
func TestAStreamCutMidAnswerTellsTheCallerItBroke(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		fmt.Fprint(rw, chunkOf("half an ans"))
		rw.(http.Flusher).Flush()
		if conn, _, err := rw.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}, never(t))

	c := w.chat(streamAsk)
	out := sent(c)
	if !strings.Contains(out, "half an ans") {
		t.Fatalf("the caller lost what it had already been sent:\n%s", out)
	}
	if !cutTold(out) {
		t.Errorf("a stream cut mid-answer ended as if it were whole — no error frame:\n%s", out)
	}
	if strings.Contains(out, "[DONE]") {
		t.Errorf("a cut stream was closed with [DONE]:\n%s", out)
	}
	if _, reserved, _, _ := object.GlobalBalanceLedger.Snapshot(w.subject); reserved != 0 {
		t.Errorf("%d cents still held after a cut stream", reserved)
	}
}

// A vendor that closes its response cleanly, but before any finish_reason or [DONE].
// Nothing at the TCP level went wrong, which is why it passes for an answer.
func TestAStreamThatStopsWithoutFinishingTellsTheCaller(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(rw, `data: {"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`+"\n\n")
		fmt.Fprint(rw, chunkOf("The answer is"))
		fmt.Fprint(rw, chunkOf(" forty"))
	}, never(t))

	out := sent(w.chat(streamAsk))
	if !strings.Contains(out, " forty") {
		t.Fatalf("the relayed text is missing:\n%s", out)
	}
	if !cutTold(out) {
		t.Errorf("a stream with no finish_reason and no [DONE] ended silently:\n%s", out)
	}
}

// Control: a stream that finished is relayed exactly as before, with no error frame.
func TestAFinishedStreamGetsNoErrorFrame(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, streams([]string{"all ", "done"}, 5, 0, 2), never(t))
	out := sent(w.chat(streamAsk))
	if !strings.Contains(out, "[DONE]") || !strings.Contains(out, `"content":"done"`) {
		t.Fatalf("want the whole stream relayed:\n%s", out)
	}
	if strings.Contains(out, "upstream_error") {
		t.Errorf("a finished stream was flagged as cut:\n%s", out)
	}
}

// Control: some OpenAI-compatible vendors send finish_reason and never [DONE]. That
// answer said it was done, and is not flagged.
func TestAStreamThatFinishesWithoutDoneIsNotFlagged(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(rw, chunkOf("complete"))
		fmt.Fprint(rw, `data: {"id":"up-1","object":"chat.completion.chunk","created":1,"model":"a-up","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	}, never(t))
	out := sent(w.chat(streamAsk))
	if !strings.Contains(out, "complete") {
		t.Fatalf("the answer is missing:\n%s", out)
	}
	if strings.Contains(out, "upstream_error") {
		t.Errorf("a stream that gave its finish_reason was flagged as cut:\n%s", out)
	}
}

// A single SSE line longer than the relay's scanner allows stops the scanner. The
// rest of the answer is lost either way; the caller must hear that it was.
func TestAnOversizedStreamLineIsReportedNotDropped(t *testing.T) {
	sse := chunkOf("start") +
		`data: {"choices":[{"delta":{"content":"` + strings.Repeat("x", 9<<20) + `"}}]}` + "\n\n" +
		chunkOf("never seen") +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	var out strings.Builder
	_, text := streamCaptureUsage(strings.NewReader(sse), &out, nil, false, nil, &mark{id: "chatcmpl-big", model: "m", seller: "hanzo"})
	if !strings.Contains(text, "start") {
		t.Fatalf("billing text = %q, want what was relayed before the long line", text)
	}
	if !strings.Contains(out.String(), "broke before the answer finished") {
		t.Errorf("a line the scanner could not hold ended the stream silently:\n%.400s", out.String())
	}
}

// ── buffered 200s that answer nothing ──────────────────────────────────────

func emptyChoices(rw http.ResponseWriter, _ int) {
	rw.Header().Set("Content-Type", "application/json")
	fmt.Fprint(rw, `{"id":"up-1","object":"chat.completion","created":1,"model":"a-up","choices":[],`+
		`"usage":{"prompt_tokens":3,"completion_tokens":0,"total_tokens":3}}`)
}

// A 200 with "choices": [] and no error is a vendor that answered nothing. It is
// moved like a refusal, and only the fallback's answer reaches the caller.
func TestABufferedAnswerWithNoChoicesFailsOver(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, emptyChoices, completes("from b", 3, 0, 2))
	c := w.chat(plainAsk)
	if answered(c) != http.StatusOK || !strings.Contains(sent(c), "from b") {
		t.Fatalf("status %d, want the fallback's answer: %s", answered(c), sent(c))
	}
	if w.a.asked() != 1 || w.b.asked() != 1 {
		t.Errorf("asked a=%d b=%d, want one each", w.a.asked(), w.b.asked())
	}
}

// Every vendor answering nothing is an honest failure, and costs the caller nothing.
func TestNoChoicesFromEveryVendorIsNotABilledSuccess(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, emptyChoices, emptyChoices)
	c := w.chat(plainAsk)
	if answered(c) == http.StatusOK {
		t.Errorf("no vendor answered and the caller was told 200: %s", sent(c))
	}
	if got := w.spent(t); got != 0 {
		t.Errorf("an answer nobody gave cost the caller %d cents", got)
	}
}

// An HTML error page served with 200 (a CDN or proxy in front of the vendor) is not
// an answer either.
func TestAnHTMLPageBehindA200FailsOver(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.Header().Set("Content-Type", "text/html")
		fmt.Fprint(rw, "<html><body><h1>502 Bad Gateway</h1></body></html>")
	}, completes("from b", 3, 0, 2))
	c := w.chat(plainAsk)
	if answered(c) != http.StatusOK || !strings.Contains(sent(c), "from b") {
		t.Fatalf("status %d, want the fallback's answer: %s", answered(c), sent(c))
	}
	if strings.Contains(sent(c), "Bad Gateway") {
		t.Errorf("the HTML page reached the caller: %s", sent(c))
	}
}

// Control: a choice whose content is "" is the model's answer, and is relayed as it
// gave it — the check is for no choices, not for short answers.
func TestAnEmptyStringAnswerIsStillAnAnswer(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, completes("", 3, 0, 0), never(t))
	c := w.chat(plainAsk)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if w.b.asked() != 0 {
		t.Error("a real (empty-string) answer was moved to the fallback")
	}
}

// ── fallback fidelity, streamed ─────────────────────────────────────────────

// The buffered relay is proven to carry the whole conversation to its first vendor
// (TestTheRelaySendsTheConversationAsWritten). A streamed request moved to its
// fallback must arrive there whole too: system prompt, tools, tool_choice,
// response_format, every turn.
func TestAStreamedFailoverCarriesTheWholeRequest(t *testing.T) {
	quick(t)
	w := newRelayWorld(t, declines(http.StatusServiceUnavailable, "overloaded"), streams([]string{`{"a":"ok"}`}, 30, 0, 5))
	body := `{"stream":true,` + strings.TrimPrefix(strings.TrimSpace(conversation), "{")

	c := w.chat(body)
	out := sent(c)
	if answered(c) != http.StatusOK || !strings.Contains(out, "[DONE]") {
		t.Fatalf("status %d, want the fallback's whole stream:\n%s", answered(c), out)
	}
	if w.b.asked() != 1 {
		t.Fatalf("the fallback was asked %d time(s), want 1", w.b.asked())
	}
	up := w.b.sent(t, 0)
	if got, asked := canonical(t, up, ourFields...), canonical(t, []byte(body), ourFields...); got != asked {
		t.Errorf("the fallback was sent a different request\ncaller:   %s\nfallback: %s", body, up)
	}
	if !strings.Contains(string(up), `"b-up"`) {
		t.Errorf("the fallback was not asked under its own upstream id: %s", up)
	}
	if strings.Contains(out, `"a-up"`) || strings.Contains(out, `"b-up"`) {
		t.Errorf("an upstream id reached the caller; the stream must name the SKU:\n%s", out)
	}
}
