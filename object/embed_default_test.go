// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import "testing"

// The default RAG embedder must resolve to a gateway URL the cloud pod reaches
// in <1s — NEVER api.openai.com (a server-side embed there hangs ~180s and the
// vector leg never persists). These pin the resolution + the self-heal policy.

func TestDefaultEmbedBaseURL(t *testing.T) {
	for _, k := range []string{"HANZO_EMBED_BASE_URL", "CLOUD_AI_BASE_URL"} {
		t.Setenv(k, "")
	}
	if got := defaultEmbedBaseURL(); got != "https://api.hanzo.ai/v1" {
		t.Fatalf("default: got %q, want https://api.hanzo.ai/v1", got)
	}
	t.Setenv("CLOUD_AI_BASE_URL", "http://gateway.hanzo.svc:8080/v1/")
	if got := defaultEmbedBaseURL(); got != "http://gateway.hanzo.svc:8080/v1" {
		t.Fatalf("CLOUD_AI_BASE_URL (trailing slash trimmed): got %q", got)
	}
	t.Setenv("HANZO_EMBED_BASE_URL", "http://embed.internal/v1")
	if got := defaultEmbedBaseURL(); got != "http://embed.internal/v1" {
		t.Fatalf("HANZO_EMBED_BASE_URL must win: got %q", got)
	}
}

func TestDefaultEmbedModelAndKey(t *testing.T) {
	t.Setenv("HANZO_EMBED_MODEL", "")
	t.Setenv("HANZO_EMBED_KEY_REF", "")
	if got := defaultEmbedModel(); got != "text-embedding-qwen3" {
		t.Fatalf("default model: got %q", got)
	}
	if got := defaultEmbedKeyRef(); got != "CLOUD_AI_API_KEY" {
		t.Fatalf("default key ref: got %q", got)
	}
	t.Setenv("HANZO_EMBED_MODEL", "text-embedding-3-large")
	t.Setenv("HANZO_EMBED_KEY_REF", "OPENAI_API_KEY")
	if got := defaultEmbedModel(); got != "text-embedding-3-large" {
		t.Fatalf("model override: got %q", got)
	}
	if got := defaultEmbedKeyRef(); got != "OPENAI_API_KEY" {
		t.Fatalf("key ref override: got %q", got)
	}
}

func TestEmbedderNeedsHeal(t *testing.T) {
	cases := []struct {
		name string
		p    *Provider
		want bool
	}{
		{"nil", nil, true},
		{"dummy", &Provider{Type: "Dummy"}, true},
		{"openai-direct-empty-url", &Provider{Type: "OpenAI", ProviderUrl: ""}, true},
		{"openai-direct-explicit", &Provider{Type: "OpenAI", ProviderUrl: "https://api.openai.com/v1"}, true},
		{"gateway-healed", &Provider{Type: "OpenAI", ProviderUrl: "https://api.hanzo.ai/v1"}, false},
		{"operator-selfhosted", &Provider{Type: "OpenAI", ProviderUrl: "http://embed.internal/v1"}, false},
	}
	for _, c := range cases {
		if got := embedderNeedsHeal(c.p); got != c.want {
			t.Errorf("%s: embedderNeedsHeal=%v, want %v", c.name, got, c.want)
		}
	}
}
