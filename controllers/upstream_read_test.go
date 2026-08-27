// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/proxy"
)

// truncating answers with a Content-Length it does not honour and then hangs up,
// which is what a connection dropped mid-answer looks like to the reader: a body
// that starts, does not finish, and fails on the read.
func truncating(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":11,"output_`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// An answer this process could not finish reading is not one it can hand over:
// truncated JSON under a 200, with the token counts parsed out of it settling the
// hold.
func TestAnUnreadableUpstreamAnswerIsNotHandedOver(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	// What boot does for the provider HTTP client; without it the SDK is handed a
	// nil client.
	proxy.InitHttpClient()

	available := int64(100000)
	t.Setenv("commerceEndpoint", fakeCommerceBalance(t, &available))
	t.Setenv("commerceToken", "test-svc-token")

	// The org's own provider for this route, native Anthropic, pointed at an
	// upstream that cuts the answer off.
	if _, err := object.AddProvider(&object.Provider{
		Owner: "acme", Name: "do-ai", Category: "Model", Type: "Anthropic",
		SubType: "claude-opus-4-5", ProviderUrl: truncating(t), ProviderKey: "k",
		State: "Active",
	}); err != nil {
		t.Fatal(err)
	}

	val := people.signedIn(t, &iam.User{Owner: "acme", Name: "val"})
	c := as(visit("POST", "/v1/messages"), val)
	// Tools, because that is what forwards the request to the upstream verbatim and
	// reads the answer back — the path this is about.
	c.Fiber().Request().SetBody([]byte(`{"model":"claude-opus-4-5","max_tokens":16,` +
		`"messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"name":"noop","description":"does nothing","input_schema":{"type":"object"}}]}`))
	c.AnthropicMessages()

	body := sent(c)
	if strings.Contains(body, `"input_tokens":11`) {
		t.Errorf("a body that could not be read was handed back: %s", body)
	}
}

// The same on the other surface, which reads the same answer the same way.
func TestTheZapSurfaceAlsoRefusesAnUnreadableAnswer(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	proxy.InitHttpClient()

	available := int64(100000)
	t.Setenv("commerceEndpoint", fakeCommerceBalance(t, &available))
	t.Setenv("commerceToken", "test-svc-token")

	if _, err := object.AddProvider(&object.Provider{
		Owner: "acme", Name: "do-ai", Category: "Model", Type: "Anthropic",
		SubType: "claude-opus-4-5", ProviderUrl: truncating(t), ProviderKey: "k",
		State: "Active",
	}); err != nil {
		t.Fatal(err)
	}

	val := people.signedIn(t, &iam.User{Owner: "acme", Name: "val"})
	_, respBody, _ := zapAnthropicMessages(context.Background(), val,
		[]byte(`{"model":"claude-opus-4-5","max_tokens":16,`+
			`"messages":[{"role":"user","content":"hi"}],`+
			`"tools":[{"name":"noop","description":"does nothing","input_schema":{"type":"object"}}]}`))

	if strings.Contains(string(respBody), `"input_tokens":11`) {
		t.Errorf("a body that could not be read was handed back: %s", respBody)
	}
}

// reporting serves one complete Anthropic answer carrying the token counts it
// wants billed for.
func reporting(t *testing.T, in, out int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"ok"}],"model":"claude-opus-4-5",`+
			`"usage":{"input_tokens":%d,"output_tokens":%d}}`, in, out)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// What the upstream says it used is what the hold settles for, and what the hold
// settles for is what leaves the balance. The reservation is an upper bound taken
// before the answer exists; settling replaces it with the real cost.
func TestTheHoldSettlesOnWhatTheUpstreamReports(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	proxy.InitHttpClient()

	available := int64(100000)
	t.Setenv("commerceEndpoint", fakeCommerceBalance(t, &available))
	t.Setenv("commerceToken", "test-svc-token")

	const in, out = 1200, 340
	if _, err := object.AddProvider(&object.Provider{
		Owner: "acme", Name: "do-ai", Category: "Model", Type: "Anthropic",
		SubType: "claude-opus-4-5", ProviderUrl: reporting(t, in, out), ProviderKey: "k",
		State: "Active",
	}); err != nil {
		t.Fatal(err)
	}

	user := &iam.User{Owner: "acme", Name: "val"}
	subject := user.PayerSubject("acme")
	object.GlobalBalanceLedger.SetBalance(subject, available)

	c := as(visit("POST", "/v1/messages"), people.signedIn(t, user))
	c.Fiber().Request().SetBody([]byte(`{"model":"claude-opus-4-5","max_tokens":16,` +
		`"messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"name":"noop","description":"does nothing","input_schema":{"type":"object"}}]}`))
	c.AnthropicMessages()

	want := calculateCostCentsWithCache("claude-opus-4-5", in, out, 0, 0)
	balance, reserved, _, known := object.GlobalBalanceLedger.Snapshot(subject)
	if !known {
		t.Fatalf("the ledger holds nothing for %q; answer was %s", subject, sent(c))
	}
	if reserved != 0 {
		t.Errorf("%d cents are still reserved after the answer", reserved)
	}
	if spent := available - balance; spent != want {
		t.Errorf("the answer cost %d cents, the ledger took %d (body %s)", want, spent, sent(c))
	}
}
