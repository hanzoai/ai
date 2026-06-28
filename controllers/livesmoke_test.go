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

//go:build livesmoke

// Live smoke layer — OPT-IN, READ-MOSTLY. Excluded from the default build, so
// the hermetic core suite never depends on live prod. Run explicitly:
//
//	HANZO_SMOKE_KEY=$(kms get …) go test -tags livesmoke -run TestLiveSmoke ./controllers/
//
// The key is read from the environment (sourced from KMS) — never hardcoded.
// HANZO_SMOKE_BASE overrides the target (default https://api.hanzo.ai). The
// suite verifies the DEPLOYED contract: the auth matrix holds in prod AND a real
// completion succeeds for a funded org (a few cents of maxpower spend).
package controllers

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func smokeBase() string {
	if v := strings.TrimRight(os.Getenv("HANZO_SMOKE_BASE"), "/"); v != "" {
		return v
	}
	return "https://api.hanzo.ai"
}

func smokeKey(t *testing.T) string {
	t.Helper()
	k := strings.TrimSpace(os.Getenv("HANZO_SMOKE_KEY"))
	if k == "" {
		t.Skip("HANZO_SMOKE_KEY not set — skipping live smoke (set from KMS to enable)")
	}
	return k
}

func smokeDo(t *testing.T, method, path, token, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, smokeBase()+path, rdr)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestLiveSmokeAuthMatrix verifies the deployed fail-secure contract at
// api.hanzo.ai: these are READ-ONLY (no charge) and confirm the matrix that the
// hermetic suite locks is actually live.
func TestLiveSmokeAuthMatrix(t *testing.T) {
	_ = smokeKey(t) // gate: only run when explicitly enabled
	const body = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"no_bearer→401", "", http.StatusUnauthorized},
		{"garbage_hk→401", "hk-not-a-real-key-000", http.StatusUnauthorized},
		{"publishable_pk→403", "pk-readonly-000", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, b := smokeDo(t, http.MethodPost, "/v1/chat/completions", tc.token, body)
			if code != tc.want {
				t.Fatalf("LIVE %s: status=%d want %d; body=%s", tc.name, code, tc.want, string(b))
			}
			if strings.Contains(string(b), "chatcmpl-") || strings.Contains(string(b), `"choices"`) {
				t.Fatalf("LIVE %s: leaked a completion on a failure path: %s", tc.name, string(b))
			}
		})
	}
}

// TestLiveSmokeModels confirms /v1/models requires auth live (401 without a
// token) and returns a non-empty list with a valid key.
func TestLiveSmokeModels(t *testing.T) {
	key := smokeKey(t)

	if code, _ := smokeDo(t, http.MethodGet, "/v1/models", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("LIVE /v1/models no-auth: status=%d want 401", code)
	}

	code, b := smokeDo(t, http.MethodGet, "/v1/models", key, "")
	if code != http.StatusOK {
		t.Fatalf("LIVE /v1/models authed: status=%d want 200; body=%s", code, string(b))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("LIVE /v1/models: decode: %v; body=%s", err, string(b))
	}
	if len(out.Data) == 0 {
		t.Fatalf("LIVE /v1/models authed: empty model list")
	}
}

// TestLiveSmokeCompletion is the deployed 200 path: a funded org gets a real
// completion (a few cents of maxpower spend). This is the success counterpart of
// the hermetic failure matrix.
func TestLiveSmokeCompletion(t *testing.T) {
	key := smokeKey(t)
	body := `{"model":"gpt-4o-mini","max_tokens":8,"messages":[{"role":"user","content":"Reply with the single word: pong"}]}`
	code, b := smokeDo(t, http.MethodPost, "/v1/chat/completions", key, body)
	if code != http.StatusOK {
		t.Fatalf("LIVE completion: status=%d want 200; body=%s", code, string(b))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("LIVE completion: decode: %v; body=%s", err, string(b))
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		t.Fatalf("LIVE completion: no content returned; body=%s", string(b))
	}
}
