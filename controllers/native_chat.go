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

// native_chat.go — a maker that speaks OpenAI's Chat Completions (native.chat).
//
// A chat request already is that dialect, so nothing is translated: the body the
// family would be sent goes to the maker under the maker's own id for the model,
// with the two things OpenAI's API reads differently from its resellers written
// its way. Its stream comes back as the family's would, and what it cost is read
// from the usage chunk it is asked to end with.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// chatBody is the body a chat maker is sent for c: the family's body under the
// vendor's id for the model, streamed with its usage, and the completion ceiling
// under the key the vendor reads. OpenAI refuses max_tokens on its reasoning
// models and reads max_completion_tokens on every chat model, so a ceiling named
// max_tokens is sent as max_completion_tokens; one the caller named that way
// stays. Anything else the caller wrote goes as written, and what the vendor
// refuses of it is the request's own answer, which sends it to the reseller
// (send), as before.
func chatBody(c nativeCall, m claude) ([]byte, bool, error) {
	if c.path != "chat/completions" {
		return nil, false, errUntranslated
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(c.body, &in) != nil || in == nil {
		return nil, false, errUntranslated
	}
	in["model"], _ = json.Marshal(m.ID)
	if v, ok := in["max_tokens"]; ok {
		if _, named := in["max_completion_tokens"]; !named {
			in["max_completion_tokens"] = v
		}
		delete(in, "max_tokens")
	}
	if c.stream {
		in["stream"] = json.RawMessage("true")
		opts := map[string]json.RawMessage{}
		if raw, ok := in["stream_options"]; ok {
			_ = json.Unmarshal(raw, &opts)
			if opts == nil {
				opts = map[string]json.RawMessage{}
			}
		}
		opts["include_usage"] = json.RawMessage("true")
		in["stream_options"], _ = json.Marshal(opts)
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, false, errUntranslated
	}
	return body, false, nil
}

// chatRefusal reads a chat maker's refusal: its status, its error code (or type),
// and whether it says the account cannot pay. OpenAI answers an account out of
// credit 429 insufficient_quota, which is the account's and not a rate limit: it
// rests the key as a 402 does.
func chatRefusal(status int, body []byte) (int, string, bool) {
	var e struct {
		Error struct {
			Type string `json:"type"`
			Code any    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	kind := e.Error.Type
	if code, ok := e.Error.Code.(string); ok && code != "" {
		kind = code
	}
	if kind == "" {
		kind = "error"
	}
	// OpenAI says an account cannot pay by its type (insufficient_quota) and names
	// which way by its code: credit_balance_exhausted, insufficient_quota,
	// billing_hard_limit_reached.
	broke := status == http.StatusPaymentRequired || e.Error.Type == "insufficient_quota" ||
		kind == "insufficient_quota" || kind == "credit_balance_exhausted" || kind == "billing_hard_limit_reached"
	return status, kind, broke
}

// chatTap relays a chat stream as it came and reads its usage as it passes, so
// what the answer cost is stated when it ends: at its last byte, or when its
// reader hangs up on it. A stream cut before its usage chunk is priced on the
// text it wrote, at four characters a token, beside the prompt it never counted.
type chatTap struct {
	io.ReadCloser
	end func(tokens)

	mu    sync.Mutex
	line  []byte
	used  tokens
	wrote int
	ended bool
}

func (t *chatTap) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range p[:n] {
		if b != '\n' {
			if len(t.line) <= sseLineMax {
				t.line = append(t.line, b)
			}
			continue
		}
		if len(t.line) <= sseLineMax {
			t.read(strings.TrimSuffix(string(t.line), "\r"))
		}
		t.line = t.line[:0]
	}
	if err != nil {
		t.finish()
	}
	return n, err
}

// read takes one line of the stream: a data line's usage, and the text it wrote.
func (t *chatTap) read(line string) {
	data, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return
	}
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return
	}
	payload := []byte(data)
	if bytes.Contains(payload, []byte(`"usage"`)) {
		sniffZenUsage(payload, &t.used)
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(payload, &chunk) == nil {
		for _, c := range chunk.Choices {
			t.wrote += len(c.Delta.Content) + len(c.Delta.Reasoning)
		}
	}
}

// Close hangs up on the vendor and, for a stream that had not ended, says what it
// used as far as it got.
func (t *chatTap) Close() error {
	err := t.ReadCloser.Close()
	t.mu.Lock()
	t.finish()
	t.mu.Unlock()
	return err
}

// finish says once what the stream used. mu is held.
func (t *chatTap) finish() {
	if t.ended {
		return
	}
	t.ended = true
	used := t.used
	if !used.reported && t.wrote > 0 {
		used.completion = max(used.completion, t.wrote/4+1)
	}
	t.end(used)
}
