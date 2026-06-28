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
	"net/http"
	"testing"
)

// surface is one OpenAI/Anthropic-compatible endpoint plus the three request
// bodies the matrix needs: a syntactically valid body with a KNOWN model, the
// same with an UNKNOWN model, and a malformed body. Body field shapes differ per
// endpoint; auth behavior must be IDENTICAL across all of them.
type surface struct {
	name        string
	path        string
	bodyKnown   string // valid JSON, model in the static route table
	bodyUnknown string // valid JSON, model NOT in the route table → 400
	bodyBad     string // broken JSON → parse error
}

func surfaces() []surface {
	return []surface{
		{
			name:        "chat",
			path:        "/v1/chat/completions",
			bodyKnown:   `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			bodyUnknown: `{"model":"nope-not-a-real-model","messages":[{"role":"user","content":"hi"}]}`,
			bodyBad:     `{"model":`,
		},
		{
			name:        "embeddings",
			path:        "/v1/embeddings",
			bodyKnown:   `{"model":"gpt-4o","input":"hi"}`,
			bodyUnknown: `{"model":"nope-not-a-real-model","input":"hi"}`,
			bodyBad:     `{"model":`,
		},
		{
			name:        "rerank",
			path:        "/v1/rerank",
			bodyKnown:   `{"model":"gpt-4o","query":"q","documents":["a","b"]}`,
			bodyUnknown: `{"model":"nope-not-a-real-model","query":"q","documents":["a"]}`,
			bodyBad:     `{"model":`,
		},
		{
			name:        "messages",
			path:        "/v1/messages",
			bodyKnown:   `{"model":"gpt-4o","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			bodyUnknown: `{"model":"nope-not-a-real-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			bodyBad:     `{"model":`,
		},
	}
}

// TestAuthStatusMatrix is the locked-forever contract for the OpenAI/Anthropic
// auth surface. For every endpoint it asserts the EXACT HTTP status for each
// credential×body combination AND that no failure path leaks completion content
// or emits a usage/billing record (fail-secure). The four credential branches
// (widget / IAM-key / JWT / provider-key) are exercised through the one shared
// policy (authResolveProvider), so this matrix pins all of them at once.
func TestAuthStatusMatrix(t *testing.T) {
	// hk-valid-1 resolves to a real org; every other key is unknown (IAM 200 +
	// data:null) → must 401. Commerce returns funds so a balance check (if ever
	// reached on these paths) is never the reason for a non-2xx.
	startIAMStub(t, map[string]iamStubUser{"hk-valid-1": {owner: "acme", name: "alice"}})
	startCommerceStub(t, 100_00)
	spy := startBillingSpy(t)
	h := newAPIHandler()

	// jwtGarbage is JWT-SHAPED (3 dot-separated segments, each > 10 chars) so it
	// takes the JWT branch, where signature validation must reject it as 401 —
	// never fall through to a 200/400.
	const jwtGarbage = "aaaaaaaaaaaaa.bbbbbbbbbbbbb.ccccccccccccc"

	type authCase struct {
		name string
		// token + which body to send (resolved per surface below).
		token string
		body  func(s surface) string
		want  int
	}
	cases := []authCase{
		{"no_bearer→401", "", func(s surface) string { return s.bodyKnown }, http.StatusUnauthorized},
		{"invalid_hk→401", "hk-not-a-real-key", func(s surface) string { return s.bodyKnown }, http.StatusUnauthorized},
		{"invalid_hk+malformed_body→401", "hk-not-a-real-key", func(s surface) string { return s.bodyBad }, http.StatusUnauthorized},
		{"garbage_jwt→401", jwtGarbage, func(s surface) string { return s.bodyKnown }, http.StatusUnauthorized},
		{"publishable_pk→403", "pk-readonly-key", func(s surface) string { return s.bodyKnown }, http.StatusForbidden},
		{"valid_hk+unknown_model→400", "hk-valid-1", func(s surface) string { return s.bodyUnknown }, http.StatusBadRequest},
		{"valid_hk+malformed_body→400", "hk-valid-1", func(s surface) string { return s.bodyBad }, http.StatusBadRequest},
	}

	for _, s := range surfaces() {
		for _, tc := range cases {
			t.Run(s.name+"/"+tc.name, func(t *testing.T) {
				w := fire(h, http.MethodPost, s.path, tc.token, tc.body(s), nil)
				if w.Code != tc.want {
					t.Fatalf("%s %s: status=%d want %d; body=%s", s.path, tc.name, w.Code, tc.want, w.Body.String())
				}
				// Fail-secure: never 200, never leak completion content.
				if w.Code == http.StatusOK {
					t.Fatalf("%s %s: returned 200 on a failure path (grant-on-failure)", s.path, tc.name)
				}
				assertNoCompletion(t, s.path+" "+tc.name, w.Body.String())
			})
		}
	}

	// No failure path above may emit a usage/billing record.
	if recs := spy.drain(); len(recs) != 0 {
		t.Fatalf("fail-secure violated: %d usage record(s) emitted on failure paths: %+v", len(recs), recs)
	}
}

// TestModelsAuthMatrix locks /v1/models: it requires a valid-format bearer (or a
// session) — an unauthenticated or malformed-format token is 401, never a model
// list. This is the read-only sibling of the completions matrix (R-04/R-RED-03).
func TestModelsAuthMatrix(t *testing.T) {
	h := newAPIHandler()
	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"no_token→401", "", http.StatusUnauthorized},
		{"garbage_format→401", "not-a-valid-token-format", http.StatusUnauthorized},
		{"two_segment_jwt→401", "aaaaaaaaaaaa.bbbbbbbbbbbb", http.StatusUnauthorized},
		{"hk_prefix→200_listed", "hk-anything", http.StatusOK}, // format-accepted; listing is public metadata
		{"pk_prefix→200_listed", "pk-anything", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := fire(h, http.MethodGet, "/v1/models", tc.token, "", nil)
			if w.Code != tc.want {
				t.Fatalf("/v1/models %s: status=%d want %d; body=%s", tc.name, w.Code, tc.want, w.Body.String())
			}
			if tc.want == http.StatusUnauthorized && w.Code == http.StatusOK {
				t.Fatalf("/v1/models %s: leaked model list without auth", tc.name)
			}
		})
	}
}

// TestValidKeyBadBodyIsClientErrorNotAuth pins the dual of the auth-before-parse
// rule: an INVALID credential with a bad body is 401 (deny, no probe), but a
// VALID credential with a bad body is the precise 400 — proving the handler
// authenticates first yet still surfaces a real client error to a real caller.
func TestValidKeyBadBodyIsClientErrorNotAuth(t *testing.T) {
	startIAMStub(t, map[string]iamStubUser{"hk-valid-1": {owner: "acme", name: "alice"}})
	startCommerceStub(t, 100_00)
	startBillingSpy(t)
	h := newAPIHandler()

	for _, s := range surfaces() {
		// invalid key + bad body → 401
		if w := fire(h, http.MethodPost, s.path, "hk-bogus", s.bodyBad, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s invalid_key+bad_body: status=%d want 401", s.path, w.Code)
		}
		// valid key + bad body → 400
		if w := fire(h, http.MethodPost, s.path, "hk-valid-1", s.bodyBad, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s valid_key+bad_body: status=%d want 400", s.path, w.Code)
		}
	}
}
