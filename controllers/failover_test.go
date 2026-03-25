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
	"fmt"
	"os"
	"testing"
)

func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"401 unauthorized", fmt.Errorf("HTTP 401 Unauthorized"), true},
		{"429 rate limit", fmt.Errorf("HTTP 429 Too Many Requests"), true},
		{"rate limit text", fmt.Errorf("rate limit exceeded"), true},
		{"500 internal", fmt.Errorf("HTTP 500 Internal Server Error"), true},
		{"502 bad gateway", fmt.Errorf("502 bad gateway"), true},
		{"503 unavailable", fmt.Errorf("service unavailable"), true},
		{"504 gateway timeout", fmt.Errorf("504 gateway timeout"), true},
		{"timeout", fmt.Errorf("request timeout exceeded"), true},
		{"deadline exceeded", fmt.Errorf("context deadline exceeded"), true},
		{"connection refused", fmt.Errorf("dial tcp: connection refused"), true},
		{"connection reset", fmt.Errorf("connection reset by peer"), true},
		{"eof", fmt.Errorf("unexpected EOF"), true},
		{"model not found", fmt.Errorf("model not found: gpt-99"), false},
		{"invalid request", fmt.Errorf("invalid request body"), false},
		{"auth error different", fmt.Errorf("invalid API key format"), false},
		{"empty error", fmt.Errorf(""), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isRetryableError(tc.err)
			if got != tc.want {
				t.Errorf("isRetryableError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestFallbacksInModelConfig(t *testing.T) {
	yaml := `
version: 1
services:
  pricing_url: "https://pricing.hanzo.ai"
cache:
  pricing_ttl: "1h"
features:
  live_mode: false
  premium_gate: true
  starter_credit: 5.00
default_pricing:
  input_per_million: 1.00
  output_per_million: 4.00
models:
  claude-opus-4-6:
    provider: do-ai
    upstream: anthropic-claude-opus-4.6
    fallbacks:
      - provider: anthropic
        upstream: claude-opus-4-6-20250514
      - provider: anthropic-backup
        upstream: claude-opus-4-6-20250514
    pricing: { input: 15.00, output: 75.00 }
  gpt-4o:
    provider: do-ai
    upstream: openai-gpt-4o
    pricing: { input: 2.50, output: 10.00 }
`

	dir := t.TempDir()
	path := dir + "/models.yaml"
	if err := writeYAML(t, path, yaml); err != nil {
		t.Fatal(err)
	}

	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		prompts: make(map[string]string),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatalf("loadFromFile failed: %v", err)
	}

	// claude-opus-4-6 should have 2 fallbacks
	route := mc.ResolveRoute("claude-opus-4-6")
	if route == nil {
		t.Fatal("expected route for claude-opus-4-6")
	}
	if route.providerName != "do-ai" {
		t.Errorf("primary provider = %q, want do-ai", route.providerName)
	}
	if len(route.fallbacks) != 2 {
		t.Fatalf("expected 2 fallbacks, got %d", len(route.fallbacks))
	}
	if route.fallbacks[0].providerName != "anthropic" {
		t.Errorf("fallback[0].providerName = %q, want anthropic", route.fallbacks[0].providerName)
	}
	if route.fallbacks[0].upstreamModel != "claude-opus-4-6-20250514" {
		t.Errorf("fallback[0].upstreamModel = %q, want claude-opus-4-6-20250514", route.fallbacks[0].upstreamModel)
	}
	if route.fallbacks[1].providerName != "anthropic-backup" {
		t.Errorf("fallback[1].providerName = %q, want anthropic-backup", route.fallbacks[1].providerName)
	}

	// gpt-4o should have no fallbacks
	route = mc.ResolveRoute("gpt-4o")
	if route == nil {
		t.Fatal("expected route for gpt-4o")
	}
	if len(route.fallbacks) != 0 {
		t.Errorf("expected 0 fallbacks for gpt-4o, got %d", len(route.fallbacks))
	}
}

func writeYAML(t *testing.T, path string, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o644)
}
