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

package crossmodel

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestCompatibilityMatrix_Live is the #138 acceptance run: every family in Matrix,
// every scenario in Scenarios, one cell each, reported as a matrix. It needs a
// credential and is skipped without one, so the compile-and-vet gate stays green.
//
//	HANZO_API_KEY=hzo_… go test ./qa/crossmodel -run Live -timeout 30m -v
//	HANZO_QA_REPORT=/tmp/matrix.json … # also writes /tmp/matrix.md beside it
func TestCompatibilityMatrix_Live(t *testing.T) {
	client := NewClient()
	if client.Key == "" {
		t.Skipf("%s not set — skipping the live cross-model matrix", KeyEnv)
	}

	var cells []Cell
	for _, f := range Matrix {
		for _, sc := range Scenarios {
			cell := Cell{Family: f.Name, Model: f.Model, Scenario: sc.Name}
			switch {
			case sc.NeedsChat && !f.Chat:
				cell.Result, cell.Detail = "skip", f.Model+" is not served on /v1/chat/completions"
			case sc.NeedsTools && !f.Tools:
				cell.Result, cell.Detail = "skip", "catalog lists no supports_tools for "+f.Model
			default:
				t.Run(f.Name+"/"+sc.Name, func(t *testing.T) {
					status, detail, problems := sc.Run(context.Background(), client, f)
					cell.Status, cell.Detail = status, detail
					if len(problems) > 0 {
						cell.Result, cell.Problems = "fail", problems
						t.Errorf("%s: %s", f.Name+"/"+sc.Name, strings.Join(problems, "\n  "))
					} else {
						cell.Result = "pass"
					}
				})
			}
			cells = append(cells, cell) // subtests run sequentially, so the order is deterministic
		}
	}

	if path := os.Getenv(ReportEnv); path != "" {
		if err := WriteReport(path, cells); err != nil {
			t.Errorf("write %s: %v", path, err)
		} else {
			t.Logf("wrote the matrix to %s and %s.md", path, strings.TrimSuffix(path, ".json"))
		}
	}
}

// TestCatalog_Live proves the matrix is still pointed at real models: every pinned id
// must exist in the public catalog, and the capability flags that drive cell skips
// must match it. This costs no credits — /v1/models is public.
func TestCatalog_Live(t *testing.T) {
	requireLive(t, false)

	client := NewClient()
	ctx, cancel := deadline(context.Background())
	defer cancel()

	reply, err := client.Models(ctx)
	if err != nil {
		t.Fatalf("GET %s/models: %v", client.Base, err)
	}
	if reply.Status != 200 {
		t.Fatalf("GET /v1/models = %d: %s", reply.Status, clip(string(reply.Body)))
	}
	listed := map[string]map[string]any{}
	for _, raw := range catalogEntries(t, reply.Body) {
		entry, _ := raw.(map[string]any)
		listed[str(entry, "id")] = entry
	}

	for _, f := range Matrix {
		entry, ok := listed[f.Model]
		if !ok {
			t.Errorf("%s: %q is no longer in GET /v1/models — retarget the matrix row", f.Name, f.Model)
			continue
		}
		if got := str(entry, "object"); got != "model" {
			t.Errorf("%s: catalog entry object = %q, want \"model\"", f.Name, got)
		}
		if ep := str(entry, "endpoint"); (ep == "/v1/chat/completions") != f.Chat {
			t.Errorf("%s: catalog endpoint %q disagrees with Chat=%v in the matrix", f.Name, ep, f.Chat)
		}
		if tools, _ := entry["supports_tools"].(bool); tools != f.Tools {
			t.Errorf("%s: catalog supports_tools = %v disagrees with Tools=%v in the matrix", f.Name, tools, f.Tools)
		}
	}
}

// TestPublicErrorEnvelopes_Live checks the error contract on the paths reachable
// without a credential, which is where every first integration call lands.
func TestPublicErrorEnvelopes_Live(t *testing.T) {
	requireLive(t, false)

	anonymous := &Client{Base: BaseURL(), HTTP: NewClient().HTTP}
	ctx, cancel := deadline(context.Background())
	defer cancel()

	t.Run("openai_no_credential", func(t *testing.T) {
		reply, err := anonymous.Post(ctx, "/chat/completions", map[string]any{
			"model": Matrix[4].Model, "messages": []any{map[string]any{"role": "user", "content": "hi"}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if reply.Status != 401 {
			t.Errorf("status = %d, want 401: %s", reply.Status, clip(string(reply.Body)))
		}
		for _, p := range CheckOpenAIErrorEnvelope(reply.Body) {
			t.Errorf("openai error envelope: %s", p)
		}
	})

	t.Run("anthropic_no_credential", func(t *testing.T) {
		reply, err := anonymous.Messages(ctx, map[string]any{
			"model": Matrix[3].Model, "max_tokens": 8,
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if reply.Status != 401 {
			t.Errorf("status = %d, want 401: %s", reply.Status, clip(string(reply.Body)))
		}
		for _, p := range CheckAnthropicErrorEnvelope(reply.Body) {
			t.Errorf("anthropic error envelope: %s", p)
		}
	})

	t.Run("unknown_route", func(t *testing.T) {
		reply, err := anonymous.Post(ctx, "/chat/completionz", map[string]any{"model": "x"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if reply.Status != 404 {
			t.Errorf("status = %d, want 404: %s", reply.Status, clip(string(reply.Body)))
		}
		for _, p := range CheckOpenAIErrorEnvelope(reply.Body) {
			t.Errorf("404 envelope: %s", p)
		}
	})
}

// TestInvalidInputErrors_Live covers the 4xx cases that need a credential to be
// reached at all: a malformed request body and an unknown model.
func TestInvalidInputErrors_Live(t *testing.T) {
	requireLive(t, true)

	client := NewClient()
	ctx, cancel := deadline(context.Background())
	defer cancel()

	t.Run("openai_malformed_messages", func(t *testing.T) {
		reply, err := client.Chat(ctx, map[string]any{"model": Matrix[4].Model, "messages": "not-a-list"})
		if err != nil {
			t.Fatal(err)
		}
		if reply.Status != 400 && reply.Status != 422 {
			t.Errorf("status = %d, want 400 or 422: %s", reply.Status, clip(string(reply.Body)))
		}
		for _, p := range CheckOpenAIErrorEnvelope(reply.Body) {
			t.Errorf("openai 4xx envelope: %s", p)
		}
	})

	t.Run("anthropic_malformed_messages", func(t *testing.T) {
		reply, err := client.Messages(ctx, map[string]any{"model": Matrix[3].Model, "messages": []any{}})
		if err != nil {
			t.Fatal(err)
		}
		if reply.Status != 400 && reply.Status != 422 {
			t.Errorf("status = %d, want 400 or 422: %s", reply.Status, clip(string(reply.Body)))
		}
		for _, p := range CheckAnthropicErrorEnvelope(reply.Body) {
			t.Errorf("anthropic 4xx envelope: %s", p)
		}
	})

	t.Run("unknown_model", func(t *testing.T) {
		reply, err := client.Chat(ctx, map[string]any{
			"model": "no-such-model-qa-138", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if reply.Status != 404 {
			t.Errorf("status = %d, want 404: %s", reply.Status, clip(string(reply.Body)))
		}
		for _, p := range CheckOpenAIErrorEnvelope(reply.Body) {
			t.Errorf("unknown-model envelope: %s", p)
		}
	})
}

// requireLive gates the tests that need egress: HANZO_QA_LIVE opts in without a
// credential, and a credential implies it (a live matrix run should also prove the
// error paths). needKey adds the credential requirement.
func requireLive(t *testing.T, needKey bool) {
	t.Helper()

	optedIn := os.Getenv(LivePublicEnv) != "" || os.Getenv(KeyEnv) != ""
	if !optedIn {
		t.Skipf("%s not set — skipping the live public checks", LivePublicEnv)
	}
	if needKey && os.Getenv(KeyEnv) == "" {
		t.Skipf("%s not set — the 4xx cases behind authentication are unreachable", KeyEnv)
	}
}

// catalogEntries pulls the data array out of a /v1/models answer.
func catalogEntries(t *testing.T, raw []byte) []any {
	t.Helper()

	body, err := decode(raw)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	if got := str(body, "object"); got != "list" {
		t.Fatalf("GET /v1/models object = %q, want \"list\"", got)
	}
	entries := arr(body, "data")
	if len(entries) == 0 {
		t.Fatal("GET /v1/models data is empty")
	}
	return entries
}
