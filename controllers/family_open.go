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

// family_open.go — a streamed answer is judged by its opening frames, not its status.
//
// A vendor that streams answers 200 before it has an answer: OpenRouter sends the
// status and keep-alive comments at once, and when the model behind it then fails
// (a :free model rate-limited upstream answers 429) the failure arrives as a data
// frame carrying `error` inside that 200. A family relaying such a vendor passes the
// same 200 on. Read by status alone, that stream is an answer: it is relayed, the
// caller's SDK finds no text in it, and the turn comes back empty.
//
// opening reads a stream up to the first frame that carries the answer. A stream that
// opens with an error frame, or ends before any answer, is handed back as a non-200
// response carrying that error, so the status decision in pipeToFamily moves it
// exactly as it moves a refusal: a free route to the pool, a priced one to its
// alternates or an honest error. Nothing has reached the caller at that point, so the
// request is still movable.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// openWait bounds how long opening reads before the answer begins. A model that
// thinks without streaming its reasoning sends keep-alive comments until it writes;
// past this the stream is relayed as it stands, comments first, so a caller's proxy
// sees bytes well inside its idle timeout.
var openWait = 20 * time.Second

// emptyAnswer is the refusal a stream that ended with no answer becomes.
const emptyAnswer = `{"error":{"message":"the model returned no answer","type":"upstream_error"}}`

// frame is what one line of a stream says about the answer.
type frame int

const (
	frameNone   frame = iota // a comment, a role, a ping, usage: nothing yet
	frameAnswer              // the answer has begun
	frameError               // an error, before any answer
	frameEnd                 // the stream ended with no answer
)

// opening reads a 200 stream up to its first answer frame, bounded by openWait, and
// returns a response that relays every byte it read and the rest. A stream that
// opened with an error or ended empty comes back as that error with its status.
func opening(resp *http.Response) *http.Response {
	// A committed answer is judged by awaitCommitted, which knows what a refusal
	// inside it cost.
	if resp == nil || resp.StatusCode != http.StatusOK || committedPlan(resp) {
		return resp
	}
	// Only an event stream has frames to judge. A whole answer to a stream request
	// is judged by its status, which is its verdict.
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.HasPrefix(ct, "text/event-stream") {
		return resp
	}
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	var head bytes.Buffer
	deadline := time.Now().Add(openWait)
	for {
		line, err := br.ReadBytes('\n')
		head.Write(line)
		switch said, payload := judge(line); said {
		case frameAnswer:
			return rejoined(resp, head.Bytes(), br)
		case frameError:
			resp.Body.Close()
			return refusal(resp, errorStatus(payload), payload)
		case frameEnd:
			resp.Body.Close()
			return refusal(resp, http.StatusBadGateway, []byte(emptyAnswer))
		}
		if err != nil {
			resp.Body.Close()
			return refusal(resp, http.StatusBadGateway, []byte(emptyAnswer))
		}
		if time.Now().After(deadline) {
			return rejoined(resp, head.Bytes(), br)
		}
	}
}

// rejoined is resp with its body replayed from what opening read, then the rest.
func rejoined(resp *http.Response, head []byte, rest io.Reader) *http.Response {
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), rest), resp.Body}
	return resp
}

// refusal is a JSON error response standing where a stream was.
func refusal(resp *http.Response, status int, body []byte) *http.Response {
	h := resp.Header.Clone()
	h.Set("Content-Type", "application/json")
	h.Del("Content-Length")
	return &http.Response{
		Status:        strconv.Itoa(status) + " " + http.StatusText(status),
		StatusCode:    status,
		Proto:         resp.Proto,
		ProtoMajor:    resp.ProtoMajor,
		ProtoMinor:    resp.ProtoMinor,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       resp.Request,
	}
}

// judge reads one SSE line in either dialect. payload is the data frame's JSON.
func judge(line []byte) (frame, []byte) {
	payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), zenDataPrefix)
	if !ok {
		return frameNone, nil
	}
	payload = bytes.TrimSpace(payload)
	if bytes.Equal(payload, []byte("[DONE]")) {
		return frameEnd, payload
	}
	var f struct {
		Type    string          `json:"type"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Delta        map[string]json.RawMessage `json:"delta"`
			FinishReason string                     `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(payload, &f) != nil {
		// A frame in a shape this does not read is the vendor's answer to relay, not
		// a failure to invent.
		return frameAnswer, payload
	}
	if present(f.Error) || f.Type == "error" {
		return frameError, payload
	}
	switch f.Type {
	case "content_block_start", "content_block_delta":
		return frameAnswer, payload
	case "message_stop":
		return frameEnd, payload
	}
	for _, c := range f.Choices {
		// Any field of the delta but its role, holding something, is the answer
		// begun: text, reasoning, a refusal, a tool call, an image.
		for k, v := range c.Delta {
			if k != "role" && present(v) {
				return frameAnswer, payload
			}
		}
		if c.FinishReason == "error" {
			return frameError, payload
		}
	}
	return frameNone, payload
}

// present reports a JSON value that holds something: not absent, null, "", [] or {}.
func present(v json.RawMessage) bool {
	switch string(bytes.TrimSpace(v)) {
	case "", "null", `""`, "[]", "{}":
		return false
	}
	return true
}

// errorStatus is the HTTP status an error frame states in error.code, or 502 when
// it states none: a vendor writes the status it would have answered with there.
func errorStatus(payload []byte) int {
	var e struct {
		Error struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &e) == nil {
		if n, err := strconv.Atoi(string(bytes.TrimSpace(e.Error.Code))); err == nil && n >= 400 && n <= 599 {
			return n
		}
	}
	return http.StatusBadGateway
}

// drainWait bounds how long a committed answer that refused is read on to its end,
// where its family states what it cost.
var drainWait = 5 * time.Second

// waiting is what awaitCommitted needs from its request beyond the answer: the plan's
// mark and spend, the settle of an attempt that refused, and the free pool's answer
// for a refusal that comes once the caller's answer is open. Each is safe to use from
// a stream's writer, after the request itself is gone.
type waiting struct {
	pm     *planMark
	spend  int64
	settle func()
	fill   func(status int, body []byte) (*http.Response, string)
}

// awaitCommitted reads a family's committed answer (committedPlan) until it begins,
// and answers it as any other family's answer would be answered at its status:
//
//   - its first answer frame (or, for a whole answer, a body that is not an error
//     object): the answer, every byte read relayed;
//   - an error, an empty end, or a cut before any answer: a refusal with that error's
//     status and no commit about it, which falls to the free pool exactly as a family's
//     refusal does. It is read on to its end first, so the mark owes what the family
//     states it cost, never past spend — nothing when no paid rung accepted it;
//   - commitWait passing before a paid rung accepted it (": paid") and before any
//     answer: the family is hung up on, which stops its walk, and the attempt is
//     unreached. A paid rung that accepted is waited for however long it takes.
//
// A stream that has not begun when openWait passes is answered then, as a stream any
// family is slow to begin is (opening): the caller's answer opens with the keep-alives
// it carries, and the commit is watched on from the stream's writer. Its answer is
// relayed when it begins; a refusal, or the hang-up, is answered there from the free
// pool (fill) — nothing but keep-alives has reached the caller by then.
func awaitCommitted(resp *http.Response, w waiting) (*http.Response, error) {
	pm := w.pm
	// owe reads a refused commit to its end and files what it owes: the cost its
	// family states, else what its mark owes already.
	owe := func(br io.Reader) {
		stop := time.AfterFunc(drainWait, func() { resp.Body.Close() })
		_, _ = io.Copy(io.Discard, br)
		stop.Stop()
		resp.Body.Close()
		if n, ok := usdNanos(resp.Trailer.Get(costHeader)); ok {
			pm.owed.Store(min(n, w.spend))
		}
	}
	refusalOf := func(status int, body []byte) *http.Response {
		out := refusal(resp, status, body)
		out.Header.Del(costBoundHeader)
		return out
	}
	if !eventStream(resp) {
		b, err := io.ReadAll(resp.Body)
		// A whole answer says a paid rung accepted it with a tab ahead of its body.
		if lead := b[:len(b)-len(bytes.TrimLeft(b, " \t\r\n"))]; bytes.IndexByte(lead, '\t') >= 0 {
			pm.paid()
		}
		if err != nil {
			resp.Body.Close()
			return refusalOf(http.StatusBadGateway, []byte(emptyAnswer)), nil
		}
		if whole := bytes.TrimSpace(b); errorObject(whole) {
			owe(resp.Body)
			return refusalOf(errorStatus(whole), whole), nil
		}
		resp.Body = io.NopCloser(bytes.NewReader(b))
		return resp, nil
	}

	var (
		mu      sync.Mutex
		opened  bool // the caller's answer is open: the watcher answers it
		decided = make(chan *http.Response, 1)
		pr, pw  = io.Pipe()
		accept  atomic.Bool // a paid rung accepted: no hang-up
		hung    atomic.Bool
	)
	hangup := time.AfterFunc(commitWait, func() {
		if !accept.Load() {
			hung.Store(true)
			resp.Body.Close()
		}
	})
	// The caller's answer, once open: the commit's headers, the watcher's bytes, and
	// the commit's trailer when its own answer is what the caller is sent.
	open := &http.Response{
		Status: resp.Status, StatusCode: resp.StatusCode, Proto: resp.Proto, ProtoMajor: resp.ProtoMajor, ProtoMinor: resp.ProtoMinor,
		Header: resp.Header.Clone(), Body: pr, Trailer: http.Header{}, Request: resp.Request,
	}
	// decide hands a verdict to the request while it still waits; false once the
	// caller's answer is open and the verdict is the watcher's to answer.
	decide := func(r *http.Response) bool {
		mu.Lock()
		defer mu.Unlock()
		if opened {
			return false
		}
		decided <- r
		return true
	}
	go func() {
		br := bufio.NewReaderSize(resp.Body, 64<<10)
		var head bytes.Buffer
		sent := 0 // how much of head the open answer has been written
		relay := func() {
			if b := head.Bytes()[sent:]; len(b) > 0 {
				_, _ = pw.Write(b)
				sent = head.Len()
			}
		}
		// answer is the open answer's end once the commit refused: the free pool's
		// answer, else the refusal itself as the stream's error.
		answer := func(status int, body []byte) {
			w.settle()
			r, route := w.fill(status, body)
			if r == nil {
				_, _ = fmt.Fprintf(pw, "data: %s\n\n", bytes.TrimSpace(body))
				_ = pw.Close()
				return
			}
			defer r.Body.Close()
			open.Trailer.Set(armHeader, route)
			if eventStream(r) {
				_, err := io.Copy(pw, r.Body)
				_ = pw.CloseWithError(err)
				return
			}
			b, _ := io.ReadAll(r.Body)
			_, _ = pw.Write(asStream(b))
			_ = pw.Close()
		}
		for {
			line, err := br.ReadBytes('\n')
			head.Write(line)
			if string(bytes.TrimSpace(line)) == paidMarker {
				accept.Store(true)
				pm.paid()
			}
			said, payload := judge(line)
			switch {
			case said == frameAnswer:
				hangup.Stop()
				if decide(rejoined(resp, head.Bytes(), br)) {
					return
				}
				relay()
				_, cerr := io.Copy(pw, br)
				for k, v := range resp.Trailer {
					open.Trailer[k] = v
				}
				_ = pw.CloseWithError(cerr)
				return
			case hung.Load():
				err := fmt.Errorf("the family did not begin its answer within %s", commitWait)
				if decide(nil) {
					return
				}
				answer(0, []byte(err.Error()))
				return
			case said == frameError, said == frameEnd, err != nil:
				hangup.Stop()
				status, body := errorStatus(payload), payload
				if said != frameError {
					status, body = http.StatusBadGateway, []byte(emptyAnswer)
				}
				owe(br)
				if decide(refusalOf(status, body)) {
					return
				}
				answer(status, body)
				return
			}
			mu.Lock()
			isOpen := opened
			mu.Unlock()
			if isOpen {
				relay()
			}
		}
	}()
	select {
	case r := <-decided:
		if r == nil {
			return nil, fmt.Errorf("the family did not begin its answer within %s", commitWait)
		}
		return r, nil
	case <-time.After(openWait):
		mu.Lock()
		if len(decided) == 0 {
			opened = true
		}
		mu.Unlock()
		if !opened {
			r := <-decided
			if r == nil {
				return nil, fmt.Errorf("the family did not begin its answer within %s", commitWait)
			}
			return r, nil
		}
		return open, nil
	}
}

// asStream is a whole chat completion as the one-chunk stream that says the same,
// for an answer the pool gave whole to a request asked as a stream.
func asStream(whole []byte) []byte {
	var c struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Usage   json.RawMessage `json:"usage"`
		Choices []struct {
			Index   int             `json:"index"`
			Message json.RawMessage `json:"message"`
			Finish  string          `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(whole, &c) != nil {
		return []byte("data: " + emptyAnswer + "\n\n")
	}
	choices := make([]map[string]any, 0, len(c.Choices))
	for _, ch := range c.Choices {
		choices = append(choices, map[string]any{"index": ch.Index, "delta": ch.Message, "finish_reason": ch.Finish})
	}
	chunk := map[string]any{"id": c.ID, "model": c.Model, "object": "chat.completion.chunk", "choices": choices}
	if len(c.Usage) > 0 {
		chunk["usage"] = c.Usage
	}
	b, _ := json.Marshal(chunk)
	return append(append([]byte("data: "), b...), []byte("\n\ndata: [DONE]\n\n")...)
}

// errorObject reports whether a whole answer is an error object, in either dialect.
func errorObject(b []byte) bool {
	var e struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	return json.Unmarshal(b, &e) == nil && (present(e.Error) || e.Type == "error")
}
