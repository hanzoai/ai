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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-json-experiment/json/jsontext"
)

// strictChat is w.chat asking to be served unchanged or refused.
func (w *relayWorld) strictChat(body string) *ApiController {
	c := as(visit(http.MethodPost, "/v1/chat/completions"), w.cred)
	c.Fiber().Request().Header.Set(strictHeader, "1")
	c.Fiber().Request().SetBody([]byte(body))
	c.ChatCompletions()
	return c
}

func header(c *ApiController, name string) string {
	return string(c.Fiber().Response().Header.Peek(name))
}

// independent is the digest computed here, apart from strict.go: the body without its
// model and stream_options.include_usage, in RFC 8785 form, hashed.
func independent(t *testing.T, body string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "model")
	if o, ok := m["stream_options"].(map[string]any); ok {
		delete(o, "include_usage")
		if len(o) == 0 {
			delete(m, "stream_options")
		}
	}
	b, _ := json.Marshal(m)
	v := jsontext.Value(b)
	if err := v.Canonicalize(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(v)
	return hex.EncodeToString(sum[:])
}

func TestAStrictRequestReachesItsVendorUnchangedAndProvesIt(t *testing.T) {
	w := newRelayWorld(t, completes(`{"a":"ok"}`, 30, 0, 5), never(t))
	c := w.strictChat(conversation)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	want := independent(t, conversation)
	if got := header(c, requestSha); got != want {
		t.Errorf("%s = %q, want %q", requestSha, got, want)
	}
	if got := header(c, upstreamSha); got != want {
		t.Errorf("%s = %q, want %q", upstreamSha, got, want)
	}
	if got := independent(t, string(w.a.sent(t, 0))); got != want {
		t.Errorf("the vendor was sent a body whose digest is %s, want %s:\n%s", got, want, w.a.sent(t, 0))
	}
	if header(c, strictHeader) != "1" {
		t.Errorf("a strict answer does not say it is one")
	}
}

// The same request written with its keys in another order, without whitespace and
// with numbers spelled differently means the same thing, and digests the same.
func TestAStrictDigestIsTheRequestsMeaningNotItsSpelling(t *testing.T) {
	w := newRelayWorld(t, completes(`{"a":"ok"}`, 30, 0, 5), never(t))
	first := w.strictChat(conversation)
	second := w.strictChat(rewritten)
	if answered(first) != http.StatusOK || answered(second) != http.StatusOK {
		t.Fatalf("status %d / %d: %s %s", answered(first), answered(second), sent(first), sent(second))
	}
	if a, b := header(first, requestSha), header(second, requestSha); a == "" || a != b {
		t.Errorf("one request, two digests: %q and %q", a, b)
	}
}

func TestAStrictRequestTheGatewayWouldChangeIsRefusedNamingWhy(t *testing.T) {
	for _, tc := range []struct {
		name, body, invariant string
	}{
		{"no ceiling named", `{"model":"relay-sku","messages":[{"role":"user","content":"hi"}]}`, "ceiling_unset"},
		{"a ceiling above the reservation", `{"model":"relay-sku","max_tokens":10000000,"messages":[{"role":"user","content":"hi"}]}`, "ceiling_lowered"},
		{"one of our own fields", `{"model":"relay-sku","max_tokens":16,"fast":true,"messages":[{"role":"user","content":"hi"}]}`, "param_dropped"},
		{"a name written twice", `{"model":"relay-sku","max_tokens":16,"seed":1,"seed":2,"messages":[{"role":"user","content":"hi"}]}`, "not_canonical"},
		{"auto-routing", `{"model":"auto","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, "route_auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRelayWorld(t, never(t), never(t))
			c := w.strictChat(tc.body)
			if answered(c) != http.StatusConflict {
				t.Fatalf("status %d, want 409: %s", answered(c), sent(c))
			}
			if got := header(c, violationHeader); got != tc.invariant {
				t.Errorf("%s = %q, want %q (%s)", violationHeader, got, tc.invariant, sent(c))
			}
			if !strings.Contains(sent(c), "strict_violation") {
				t.Errorf("the refusal does not carry its code: %s", sent(c))
			}
		})
	}
}

// A vendor that refuses a strict request is the answer: it is not offered to the
// route's fallback, and nothing is billed for it.
func TestAStrictRequestIsNeverMoved(t *testing.T) {
	w := newRelayWorld(t, func(rw http.ResponseWriter, _ int) {
		rw.WriteHeader(http.StatusServiceUnavailable)
		_, _ = rw.Write([]byte(`{"error":{"message":"overloaded"}}`))
	}, never(t))
	c := w.strictChat(`{"model":"relay-sku","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if answered(c) == http.StatusOK {
		t.Fatalf("a refused strict request answered 200: %s", sent(c))
	}
	if w.b.asked() != 0 {
		t.Errorf("a strict request was moved to the fallback")
	}
	if got := w.spent(t); got != 0 {
		t.Errorf("a refused strict request cost %d cents", got)
	}
}

// A stream is billed from the usage it asks for, so include_usage is the one field
// the gateway may add, and the digests still agree.
func TestAStrictStreamProvesItsBodyWithUsageAsked(t *testing.T) {
	w := newRelayWorld(t, streams([]string{"o", "k"}, 10, 0, 2), never(t))
	body := `{"model":"relay-sku","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	c := w.strictChat(body)
	_ = sent(c)
	if answered(c) != http.StatusOK {
		t.Fatalf("status %d: %s", answered(c), sent(c))
	}
	if a, b := header(c, requestSha), header(c, upstreamSha); a == "" || a != b || a != independent(t, body) {
		t.Errorf("digests %q / %q, want both %q", a, b, independent(t, body))
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(w.a.sent(t, 0), &fields)
	if string(fields["stream_options"]) != `{"include_usage":true}` {
		t.Errorf("stream_options = %s, want usage asked of the vendor", fields["stream_options"])
	}
}

// /v1/responses is served as a chat completion, which is a translation.
func TestAStrictResponsesRequestIsRefused(t *testing.T) {
	w := newRelayWorld(t, never(t), never(t))
	c := as(visit(http.MethodPost, "/v1/responses"), w.cred)
	c.Fiber().Request().Header.Set(strictHeader, "1")
	c.Fiber().Request().SetBody([]byte(`{"model":"relay-sku","input":"hi","max_output_tokens":16}`))
	c.Responses()
	if answered(c) != http.StatusConflict || header(c, violationHeader) != "translation" {
		t.Fatalf("status %d violation %q: %s", answered(c), header(c, violationHeader), sent(c))
	}
}
