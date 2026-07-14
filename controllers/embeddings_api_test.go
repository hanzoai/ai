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
	"encoding/json"
	"math"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestResolveEndpointForPath asserts the single per-provider endpoint map
// produces correct URLs for every API path AND that the chat/completions URLs
// are byte-for-byte identical to the pre-refactor resolveUpstreamEndpoint — so
// the proven chat gateway is unchanged while embeddings/rerank are added.
func TestResolveEndpointForPath(t *testing.T) {
	cases := []struct {
		name     string
		provider object.Provider
		apiPath  string
		wantURL  string
		wantAuth string // full Authorization header, "" when Bearer apiKey is used
	}{
		// chat parity (must equal old resolveUpstreamEndpoint output)
		{"openai-chat", object.Provider{Type: "OpenAI", ProviderUrl: "https://api.openai.com/v1", ClientSecret: "k"}, "chat/completions", "https://api.openai.com/v1/chat/completions", ""},
		{"openai-no-url-chat", object.Provider{Type: "OpenAI", ClientSecret: "k"}, "chat/completions", "https://api.openai.com/v1/chat/completions", ""},
		{"fireworks-chat", object.Provider{Type: "Fireworks", ClientSecret: "k"}, "chat/completions", "https://api.fireworks.ai/inference/v1/chat/completions", ""},
		{"grok-chat", object.Provider{Type: "Grok", ClientSecret: "k"}, "chat/completions", "https://api.x.ai/v1/chat/completions", ""},
		{"openrouter-chat", object.Provider{Type: "OpenRouter", ClientSecret: "k"}, "chat/completions", "https://openrouter.ai/api/v1/chat/completions", ""},
		{"moonshot-chat", object.Provider{Type: "Moonshot", ClientSecret: "k"}, "chat/completions", "https://api.moonshot.cn/v1/chat/completions", ""},
		{"gemini-chat", object.Provider{Type: "Gemini", ClientSecret: "k"}, "chat/completions", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", ""},
		{"azure-chat", object.Provider{Type: "Azure", ProviderUrl: "https://x.openai.azure.com", SubType: "dep1", ClientSecret: "k"}, "chat/completions", "https://x.openai.azure.com/openai/deployments/dep1/chat/completions?api-version=2024-02-01", "api-key k"},
		{"do-chat", object.Provider{Type: "DigitalOcean", ProviderUrl: "https://inference.do-ai.run/v1", ClientSecret: "k"}, "chat/completions", "https://inference.do-ai.run/v1/chat/completions", ""},
		{"local-no-v1-chat", object.Provider{Type: "Local", ProviderUrl: "http://host:11434", ClientSecret: "k"}, "chat/completions", "http://host:11434/v1/chat/completions", ""},

		// embeddings
		{"openai-embed", object.Provider{Type: "OpenAI", ProviderUrl: "https://api.openai.com/v1", ClientSecret: "k"}, "embeddings", "https://api.openai.com/v1/embeddings", ""},
		{"fireworks-embed", object.Provider{Type: "Fireworks", ClientSecret: "k"}, "embeddings", "https://api.fireworks.ai/inference/v1/embeddings", ""},
		{"do-embed", object.Provider{Type: "DigitalOcean", ProviderUrl: "https://inference.do-ai.run/v1", ClientSecret: "k"}, "embeddings", "https://inference.do-ai.run/v1/embeddings", ""},
		{"azure-embed", object.Provider{Type: "Azure", ProviderUrl: "https://x.openai.azure.com", SubType: "embdep", ClientSecret: "k"}, "embeddings", "https://x.openai.azure.com/openai/deployments/embdep/embeddings?api-version=2024-02-01", "api-key k"},
		{"jina-embed", object.Provider{Type: "Jina", ClientSecret: "k"}, "embeddings", "https://api.jina.ai/v1/embeddings", ""},

		// rerank
		{"jina-rerank", object.Provider{Type: "Jina", ClientSecret: "k"}, "rerank", "https://api.jina.ai/v1/rerank", ""},
		{"cohere-rerank", object.Provider{Type: "Cohere", ClientSecret: "k"}, "rerank", "https://api.cohere.com/v1/rerank", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.provider
			url, apiKey, authHeader := resolveEndpointForPath(&p, tc.apiPath)
			if url != tc.wantURL {
				t.Fatalf("url = %q, want %q", url, tc.wantURL)
			}
			if authHeader != tc.wantAuth {
				t.Fatalf("authHeader = %q, want %q", authHeader, tc.wantAuth)
			}
			if tc.wantAuth == "" && apiKey != "k" {
				t.Fatalf("apiKey = %q, want %q", apiKey, "k")
			}
		})
	}
}

// TestResolveUpstreamEndpointAliasesChat proves the chat wrapper delegates to
// resolveEndpointForPath, so there is exactly one provider endpoint map.
func TestResolveUpstreamEndpointAliasesChat(t *testing.T) {
	p := object.Provider{Type: "OpenAI", ProviderUrl: "https://api.openai.com/v1", ClientSecret: "k"}
	wantURL, wantKey, wantAuth := resolveEndpointForPath(&p, "chat/completions")
	gotURL, gotKey, gotAuth := resolveUpstreamEndpoint(&p)
	if gotURL != wantURL || gotKey != wantKey || gotAuth != wantAuth {
		t.Fatalf("resolveUpstreamEndpoint diverged from resolveEndpointForPath: got (%q,%q,%q) want (%q,%q,%q)",
			gotURL, gotKey, gotAuth, wantURL, wantKey, wantAuth)
	}
}

// TestSetJSONModel rewrites the model field and preserves every other field.
func TestSetJSONModel(t *testing.T) {
	in := []byte(`{"model":"text-embedding-3-small","input":["a","b"],"encoding_format":"float","dimensions":256}`)
	out, err := setJSONModel(in, "text-embedding-3-large")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "text-embedding-3-large" {
		t.Fatalf("model = %v, want text-embedding-3-large", m["model"])
	}
	if m["encoding_format"] != "float" {
		t.Fatalf("encoding_format not preserved: %v", m["encoding_format"])
	}
	if m["dimensions"].(float64) != 256 {
		t.Fatalf("dimensions not preserved: %v", m["dimensions"])
	}
	input, ok := m["input"].([]interface{})
	if !ok || len(input) != 2 || input[0] != "a" {
		t.Fatalf("input not preserved: %v", m["input"])
	}
}

// TestCoerceDocText accepts both bare strings and {text} objects.
func TestCoerceDocText(t *testing.T) {
	if got := coerceDocText(json.RawMessage(`"hello"`)); got != "hello" {
		t.Fatalf("string doc = %q", got)
	}
	if got := coerceDocText(json.RawMessage(`{"text":"world"}`)); got != "world" {
		t.Fatalf("object doc = %q", got)
	}
}

// TestIsNativeRerankProvider gates which providers are proxied vs synthesized.
func TestIsNativeRerankProvider(t *testing.T) {
	for _, ty := range []string{"Jina", "Cohere", "Voyage"} {
		if !isNativeRerankProvider(ty) {
			t.Fatalf("%s should be a native rerank provider", ty)
		}
	}
	for _, ty := range []string{"OpenAI", "DigitalOcean", "Fireworks", ""} {
		if isNativeRerankProvider(ty) {
			t.Fatalf("%s should NOT be a native rerank provider", ty)
		}
	}
}

// TestCosineSimilarity checks identity, orthogonality, and the undefined cases.
func TestCosineSimilarity(t *testing.T) {
	if s := cosineSimilarity([]float64{1, 0}, []float64{1, 0}); math.Abs(s-1) > 1e-9 {
		t.Fatalf("identical vectors cosine = %v, want 1", s)
	}
	if s := cosineSimilarity([]float64{1, 0}, []float64{0, 1}); math.Abs(s) > 1e-9 {
		t.Fatalf("orthogonal vectors cosine = %v, want 0", s)
	}
	if s := cosineSimilarity([]float64{1, 2, 3}, []float64{1, 2}); s != 0 {
		t.Fatalf("mismatched length cosine = %v, want 0", s)
	}
	if s := cosineSimilarity([]float64{0, 0}, []float64{1, 1}); s != 0 {
		t.Fatalf("zero vector cosine = %v, want 0", s)
	}
}

// TestRankByCosine ranks documents by similarity to the query, most-similar
// first, preserving original document indices.
func TestRankByCosine(t *testing.T) {
	query := []float64{1, 0}
	docs := [][]float64{
		{0, 1},     // index 0 — orthogonal (score 0)
		{1, 0},     // index 1 — identical (score 1)
		{0.7, 0.7}, // index 2 — 45° (score ~0.707)
	}
	ranked := rankByCosine(query, docs)
	if len(ranked) != 3 {
		t.Fatalf("ranked len = %d", len(ranked))
	}
	if ranked[0].Index != 1 {
		t.Fatalf("top doc index = %d, want 1", ranked[0].Index)
	}
	if ranked[1].Index != 2 {
		t.Fatalf("second doc index = %d, want 2", ranked[1].Index)
	}
	if ranked[2].Index != 0 {
		t.Fatalf("third doc index = %d, want 0", ranked[2].Index)
	}
	// Scores must be in descending order.
	if !(ranked[0].Score >= ranked[1].Score && ranked[1].Score >= ranked[2].Score) {
		t.Fatalf("scores not descending: %+v", ranked)
	}
}
