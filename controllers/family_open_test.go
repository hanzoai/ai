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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/decimal"
)

// busyStream is what OpenRouter streams when the :free model behind a route is
// rate-limited upstream after the status went out: a 200, keep-alive comments, and
// the refusal as a data frame.
const busyStream = ": OPENROUTER PROCESSING\n\n" +
	`data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"v/nano:free","provider":"Nvidia",` +
	`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"error"}],` +
	`"error":{"code":429,"message":"v/nano:free is temporarily rate-limited upstream. Please retry shortly."}}` + "\n\n" +
	"data: [DONE]\n\n"

// hollowStream opens, says nothing, and ends.
const hollowStream = ": OPENROUTER PROCESSING\n\n" +
	`data: {"id":"gen-2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"gen-2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":159,"completion_tokens":0,"total_tokens":159}}` + "\n\n" +
	"data: [DONE]\n\n"

// answerStream is an answer.
const answerStream = ": OPENROUTER PROCESSING\n\n" +
	`data: {"id":"gen-3","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
	`data: {"id":"gen-3","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n" +
	`data: {"id":"gen-3","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}` + "\n\n" +
	"data: [DONE]\n\n"

func streaming(t *testing.T, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

// freeLaneTo is the free lane's own route, enso-auto, served by a family at url.
func freeLaneTo(t *testing.T, url string) *modelFamily {
	t.Helper()
	fam := otherFamily(t, url)
	fam.byID = map[string]zenModel{"enso-auto": {ID: "enso-auto"}} // discovered at zero: the free lane
	fam.ids = []string{"enso-auto"}
	return fam
}

// poolAnswers points the platform pool at a vendor that streams an answer.
func poolAnswers(t *testing.T) {
	t.Helper()
	restore(t, engineFam)
	engineFam.urlKey = "TEST_ENGINE_URL_UNSET"
	engineFam.providerFn = nil
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "")
	t.Setenv("OPENROUTER_API_KEY_3", "")
	forgetKeys()
	cooled.forget()
	spareFamily(t, streaming(t, answerStream).URL, "vendor/big:free")
}

func streamTurn(t *testing.T, fam *modelFamily, model string) (*ApiController, []attempt) {
	t.Helper()
	body := []byte(`{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"Explain what this code does"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	out := c.pipeToFamily(fam, "chat/completions", "openai", model, body, true, 0, "acme", nil, false, nil, time.Now())
	return c, out
}

// A FREE ROUTE WHOSE STREAM OPENS WITH AN ERROR IS SERVED BY THE POOL. The enso
// service relays OpenRouter's 200 whole; the rate limit inside it is a refusal, and a
// busy free route moves to the pool as it does when the 429 is the status.
func TestAFreeRouteThatStreamsAnErrorIsServedByThePool(t *testing.T) {
	poolAnswers(t)
	c, out := streamTurn(t, freeLaneTo(t, streaming(t, busyStream).URL), "enso-auto")
	if out != nil {
		t.Fatalf("attempts=%+v, want the pool to serve", out)
	}
	got := sent(c)
	if answered(c) != http.StatusOK || !strings.Contains(got, `"content":"ok"`) {
		t.Fatalf("status %d body %s, want the pool's streamed answer", answered(c), got)
	}
	if strings.Contains(got, "rate-limited upstream") {
		t.Fatalf("the refused stream reached the caller:\n%s", got)
	}
}

// A STREAM THAT ENDS WITHOUT A WORD IS NOT AN ANSWER. It is moved like a vendor
// failure, never relayed as an empty turn.
func TestAStreamThatEndsEmptyIsNotAnAnswer(t *testing.T) {
	poolAnswers(t)
	c, out := streamTurn(t, freeLaneTo(t, streaming(t, hollowStream).URL), "enso-auto")
	if out != nil {
		t.Fatalf("attempts=%+v, want the pool to serve", out)
	}
	if got := sent(c); answered(c) != http.StatusOK || !strings.Contains(got, `"content":"ok"`) {
		t.Fatalf("status %d body %s, want the pool's streamed answer", answered(c), got)
	}
}

// A PRICED ROUTE WHOSE STREAM OPENS WITH AN ERROR is not swapped for a free model,
// and not relayed as a 200 with nothing in it either.
func TestAPricedStreamThatOpensWithAnErrorIsNeverAnEmptyAnswer(t *testing.T) {
	poolAnswers(t)
	fam := otherFamily(t, streaming(t, busyStream).URL)
	flash := zenModel{ID: "enso-flash"}
	flash.Base.In, flash.Base.Out = decimal.New(3, 0), decimal.New(15, 0)
	fam.byID = map[string]zenModel{"enso-flash": flash}
	fam.ids = []string{"enso-flash"}

	c, out := streamTurn(t, fam, "enso-flash")
	got := sent(c)
	if strings.Contains(got, `"content":"ok"`) {
		t.Fatalf("a priced route was answered by the free pool:\n%s", got)
	}
	if len(out) == 0 && answered(c) == http.StatusOK {
		t.Fatalf("status 200 body %q, want the refusal handed back or answered as one", got)
	}
	if len(out) > 0 && out[0].status != http.StatusTooManyRequests {
		t.Fatalf("attempt status %d, want the 429 the frame stated", out[0].status)
	}
}

// An answer is relayed byte for byte: opening reads ahead and gives back all of it.
func TestAnAnsweringStreamIsRelayedWhole(t *testing.T) {
	resp, err := http.Get(streaming(t, answerStream).URL)
	if err != nil {
		t.Fatal(err)
	}
	got := opening(resp)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want the answer's 200", got.StatusCode)
	}
	b, _ := io.ReadAll(got.Body)
	_ = got.Body.Close()
	if string(b) != answerStream {
		t.Fatalf("relayed\n%q\nwant\n%q", b, answerStream)
	}
}

// A model still thinking past openWait is relayed as it stands: the keep-alives go
// out rather than the caller's proxy timing the request out.
func TestAStreamStillThinkingPastTheWaitIsRelayed(t *testing.T) {
	was := openWait
	openWait = 20 * time.Millisecond
	t.Cleanup(func() { openWait = was })

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for range 10 {
			_, _ = w.Write([]byte(": OPENROUTER PROCESSING\n\n"))
			f.Flush()
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(s.Close)

	resp, err := http.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := opening(resp)
	if waited := time.Since(start); waited > 80*time.Millisecond {
		t.Fatalf("opening held the stream %v, want it released at the wait", waited)
	}
	b, _ := io.ReadAll(got.Body)
	_ = got.Body.Close()
	if got.StatusCode != http.StatusOK || !strings.HasPrefix(string(b), ": OPENROUTER PROCESSING") || !strings.Contains(string(b), `"content":"ok"`) {
		t.Fatalf("status %d body %q, want every byte relayed", got.StatusCode, b)
	}
}

func TestJudgeReadsBothDialects(t *testing.T) {
	for _, tc := range []struct {
		line string
		want frame
	}{
		{": OPENROUTER PROCESSING", frameNone},
		{"event: message_start", frameNone},
		{"", frameNone},
		{`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, frameNone},
		{`data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}`, frameAnswer},
		{`data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}`, frameAnswer},
		{`data: {"choices":[{"index":0,"delta":{"reasoning":"thinking"}}]}`, frameAnswer},
		{`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1"}]}}]}`, frameAnswer},
		{`data: {"choices":[{"index":0,"delta":{"tool_calls":null}}]}`, frameNone},
		{`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null,"refusal":null,"tool_calls":[]}}]}`, frameNone},
		{`data: {"choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:x"}}]}}]}`, frameAnswer},
		{`data: {"choices":[{"index":0,"delta":{"refusal":"no"}}]}`, frameAnswer},
		{`data: {"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}`, frameError},
		{`data: {"error":{"code":429,"message":"busy"}}`, frameError},
		{`data: {"error":null,"choices":[{"index":0,"delta":{"content":"ok"}}]}`, frameAnswer},
		{`data: {"error":{"message":"the model returned an unusable answer","type":"zen_error","code":502}}`, frameError},
		{"data: [DONE]", frameEnd},
		{`data: {"type":"message_start","message":{"id":"msg_1"}}`, frameNone},
		{`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, frameAnswer},
		{`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`, frameAnswer},
		{`data: {"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, frameError},
		{`data: {"type":"message_stop"}`, frameEnd},
		{`data: {"choices":[{"delta":{"content":[{"type":"text","text":"ok"}]}}]}`, frameAnswer},
	} {
		if got, _ := judge([]byte(tc.line)); got != tc.want {
			t.Errorf("judge(%s) = %d, want %d", tc.line, got, tc.want)
		}
	}
}

func TestAnErrorFrameStatesItsStatus(t *testing.T) {
	for payload, want := range map[string]int{
		`{"error":{"code":429,"message":"busy"}}`:              http.StatusTooManyRequests,
		`{"error":{"code":502,"type":"zen_error"}}`:            http.StatusBadGateway,
		`{"error":{"code":"server_error","message":"x"}}`:      http.StatusBadGateway,
		`{"error":{"message":"no code"}}`:                      http.StatusBadGateway,
		`{"error":{"code":200,"message":"not an error"}}`:      http.StatusBadGateway,
		`{"type":"error","error":{"type":"overloaded_error"}}`: http.StatusBadGateway,
	} {
		if got := errorStatus([]byte(payload)); got != want {
			t.Errorf("errorStatus(%s) = %d, want %d", payload, got, want)
		}
	}
}
