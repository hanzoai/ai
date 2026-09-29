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
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// What is written before the stream exists is kept, goes out first when it is
// attached, and everything after passes straight through in order.
func TestUnsentKeepsTheFirstBytesUntilTheStreamExists(t *testing.T) {
	h := newUnsent()
	if h.opened() {
		t.Fatal("nothing was written and it says it opened")
	}
	if _, err := h.Write(nil); err != nil || h.opened() {
		t.Fatal("an empty write is not a first byte")
	}
	_, _ = h.Write([]byte("data: a\n\n"))
	if !h.opened() {
		t.Fatal("a byte was written and it does not say so")
	}
	select {
	case <-h.first:
	default:
		t.Fatal("first is not closed after the first byte")
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("a flush with nowhere to go is not a failure: %v", err)
	}

	var got bytes.Buffer
	w := bufio.NewWriter(&got)
	if err := h.attach(w); err != nil {
		t.Fatal(err)
	}
	if got.String() != "data: a\n\n" {
		t.Fatalf("attach delivered %q, want the kept bytes flushed", got.String())
	}
	_, _ = h.Write([]byte("data: b\n\n"))
	_ = h.Flush()
	if got.String() != "data: a\n\ndata: b\n\n" {
		t.Fatalf("after attach: %q", got.String())
	}
}

// The producer writes from its own goroutine while zip attaches from the stream
// callback. Run under -race: every byte arrives once, in order.
func TestUnsentAcrossTwoGoroutines(t *testing.T) {
	h := newUnsent()
	var want strings.Builder
	for i := range 200 {
		fmt.Fprintf(&want, "data: %d\n\n", i)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			_, _ = fmt.Fprintf(h, "data: %d\n\n", i)
			_ = h.Flush()
		}
	}()
	<-h.first
	var got bytes.Buffer
	w := bufio.NewWriter(&got)
	_ = h.attach(w)
	<-done
	_ = w.Flush()
	if got.String() != want.String() {
		t.Fatalf("delivered %d bytes, want %d in order", got.Len(), want.Len())
	}
}

// Through fiber's own serve path, where the request is released the moment the
// handler returns and the stream is written after: a handler that waits for the
// first byte and then hands over the unsent writer delivers the whole answer.
func TestAnUnsentAnswerIsDeliveredAfterTheHandlerReturns(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(http.MethodPost, "/stream", func(c *zip.Ctx) error {
		out := newUnsent()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = out.Write([]byte("data: one\n\n"))
			time.Sleep(20 * time.Millisecond)
			_, _ = out.Write([]byte("data: two\n\n"))
			_ = out.Flush()
		}()
		<-out.first
		c.SetHeader("Content-Type", "text/event-stream")
		return c.SendStreamWriter(func(bw *bufio.Writer) {
			_ = out.attach(bw)
			<-done
		})
	})
	res, err := app.Test(httptest.NewRequest(http.MethodPost, "/stream", nil), zip.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != "data: one\n\ndata: two\n\n" {
		t.Fatalf("%d %q", res.StatusCode, body)
	}
}

// A stream that fails after it began ends with one OpenAI error object and the
// end marker, so a client reading events is told why rather than left with an
// answer that simply stops.
func TestAStreamThatFailsAfterItBeganSaysWhy(t *testing.T) {
	var got bytes.Buffer
	w := bufio.NewWriter(&got)
	streamOpenAIError(w, exhausted("enso-flash", []attempt{{provider: "enso", status: 402, err: errors.New("Insufficient credits.")}}))
	frames := strings.Split(strings.TrimSpace(got.String()), "\n\n")
	if len(frames) != 2 || frames[1] != "data: [DONE]" {
		t.Fatalf("frames = %q, want one error event and the end marker", frames)
	}
	var ev struct {
		Error struct{ Message, Type, Code string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[0], "data: ")), &ev); err != nil {
		t.Fatalf("not JSON: %q", frames[0])
	}
	if !strings.Contains(ev.Error.Message, "enso (402)") || ev.Error.Type != "api_error" || ev.Error.Code != codeExhausted {
		t.Fatalf("error event = %+v", ev.Error)
	}
	if got := openAIErrorType(http.StatusTooManyRequests); got != "rate_limit_error" {
		t.Fatalf("429 is %q", got)
	}
}

// A model call nothing answered is never a 200: a vendor's 402 is our 503, a rate
// limit stays a 429, and an untyped failure is a 500 rather than an auth refusal.
func TestAModelFailureCarriesItsStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{exhausted("enso-flash", []attempt{{provider: "enso", status: 402, err: errors.New("spent")}}), http.StatusServiceUnavailable},
		{billingError("our vendor is spent"), http.StatusServiceUnavailable},
		{busyError("slow down"), http.StatusTooManyRequests},
		{errors.New("socket closed"), http.StatusInternalServerError},
	} {
		c := visit(http.MethodPost, "/v1/chat/completions")
		c.ResponseModelFailure(tc.err)
		if answered(c) != tc.want {
			t.Errorf("%v: status %d, want %d", tc.err, answered(c), tc.want)
		}
		var body Response
		if err := json.Unmarshal([]byte(sent(c)), &body); err != nil || body.Status != "error" || body.Msg != tc.err.Error() {
			t.Errorf("%v: body %s", tc.err, sent(c))
		}
	}
}

// The answer is produced after chatCompletions has handed its request back to
// fiber, so `complete` reads the snapshot and never the controller. The one
// exception is the non-streaming body, which is written inside the handler.
func TestTheAnswerReadsNoReleasedRequest(t *testing.T) {
	body := handlerBody(t, "openai_api.go", "chatCompletions")
	var complete *ast.FuncLit
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name == "complete" {
			complete, _ = as.Rhs[0].(*ast.FuncLit)
		}
		return complete == nil
	})
	if complete == nil {
		t.Fatal("chatCompletions no longer builds its answer in `complete`; this test needs revisiting")
	}
	ast.Inspect(complete.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "c" && sel.Sel.Name != "answerBody" {
			t.Errorf("complete reads c.%s; a stream runs after fiber released the request", sel.Sel.Name)
		}
		return true
	})
}
