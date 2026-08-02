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

func TestForceGatewayEmbedder(t *testing.T) {
	t.Setenv("HANZO_EMBED_BASE_URL", "")
	t.Setenv("HANZO_EMBED_MODEL", "")
	t.Setenv("HANZO_EMBED_KEY_REF", "")

	// No gateway base configured → never touch the provider (standalone ai).
	t.Setenv("CLOUD_AI_BASE_URL", "")
	p := &Provider{Type: "OpenAI", ProviderUrl: "", ClientSecret: "kms://OPENAI_API_KEY"}
	forceGatewayEmbedder(p)
	if p.ProviderUrl != "" {
		t.Fatalf("no gateway base configured: provider must be untouched, got url=%q", p.ProviderUrl)
	}

	// Gateway base configured + broken openai-direct default → forced onto gateway.
	// Key env unset → fall back to the kms:// ref (real KMS resolves downstream).
	t.Setenv("CLOUD_AI_BASE_URL", "https://api.hanzo.ai/v1/")
	t.Setenv("CLOUD_AI_API_KEY", "")
	p = &Provider{Type: "OpenAI", ProviderUrl: "", ClientSecret: "kms://OPENAI_API_KEY"}
	forceGatewayEmbedder(p)
	if p.ProviderUrl != "https://api.hanzo.ai/v1" {
		t.Fatalf("broken default: want gateway url, got %q", p.ProviderUrl)
	}
	if p.SubType != "text-embedding-qwen3" || p.ClientSecret != "kms://CLOUD_AI_API_KEY" {
		t.Fatalf("broken default (key env unset): want qwen3+kms ref, got subType=%q secret=%q", p.SubType, p.ClientSecret)
	}

	// Key env SET → resolve it in place (env-first), not a bare kms:// ref.
	t.Setenv("CLOUD_AI_API_KEY", "sk-test-embed-key")
	p = &Provider{Type: "OpenAI", ProviderUrl: "", ClientSecret: "kms://OPENAI_API_KEY"}
	forceGatewayEmbedder(p)
	if p.ClientSecret != "sk-test-embed-key" {
		t.Fatalf("broken default (key env set): want resolved key, got %q", p.ClientSecret)
	}
	t.Setenv("CLOUD_AI_API_KEY", "")

	// explicit api.openai.com URL → also forced.
	p = &Provider{Type: "OpenAI", ProviderUrl: "https://api.openai.com/v1", ClientSecret: "kms://OPENAI_API_KEY"}
	forceGatewayEmbedder(p)
	if p.ProviderUrl != "https://api.hanzo.ai/v1" {
		t.Fatalf("openai.com url: want gateway url, got %q", p.ProviderUrl)
	}

	// healthy custom embedder → left untouched even with a gateway base configured.
	p = &Provider{Type: "OpenAI", ProviderUrl: "http://embed.internal/v1", SubType: "custom", ClientSecret: "kms://CUSTOM"}
	forceGatewayEmbedder(p)
	if p.ProviderUrl != "http://embed.internal/v1" || p.SubType != "custom" {
		t.Fatalf("custom embedder must be untouched, got url=%q subType=%q", p.ProviderUrl, p.SubType)
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
