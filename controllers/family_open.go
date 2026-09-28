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
	"io"
	"net/http"
	"strconv"
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
	if resp == nil || resp.StatusCode != http.StatusOK {
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
