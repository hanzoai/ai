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

package model

import "testing"

// Every row is a named upstream, so no table can be keyed by a spelling the
// constants do not hold. The reverse does not hold: the names are shared with
// embeddings, and Jina and Word2Vec serve embeddings only.
func TestEveryRowIsANamedUpstream(t *testing.T) {
	named := map[Upstream]bool{}
	for _, v := range []Upstream{
		AlibabaCloud, AmazonBedrock, Anthropic, Azure, Baichuan, BaiduCloud,
		ChatGLM, Cohere, DeepSeek, DigitalOcean, Dummy, Enso, Fireworks, Gemini,
		GitHub, Grok, Hanzo, HuggingFace, IFlytek, Jina, Local, MiniMax,
		Mistral, Moonshot, Ollama, OpenAI, OpenRouter, SiliconFlow, StepFun,
		TencentCloud, VolcanoEngine, Word2Vec, Writer, Yi, Zen,
	} {
		if named[v] {
			t.Errorf("upstream %q is named twice", v)
		}
		named[v] = true
	}
	for v := range upstreams {
		if !named[v] {
			t.Errorf("the table is keyed by %q, which is not a named upstream", v)
		}
	}
	if len(upstreams) == 0 {
		t.Fatal("no upstreams — this check is reading nothing")
	}
}

// An upstream this build does not speak to is absent, not an error — the answer a
// row that is not there gives, which the caller reports as "not supported".
func TestAnUnknownUpstreamIsAbsentRatherThanAnError(t *testing.T) {
	made, err := Open(Spec{Upstream: "Nobody", Model: "m"})
	if err != nil {
		t.Errorf("an unknown upstream answered with an error: %v", err)
	}
	if made != nil {
		t.Errorf("an unknown upstream answered with a provider: %#v", made)
	}
}

// Anthropic serves Claude: the row names the upstream, the model names the model.
func TestAnthropicIsTheUpstreamNotTheModel(t *testing.T) {
	if Anthropic != "Anthropic" {
		t.Errorf("the upstream is spelled %q", Anthropic)
	}
	made, err := Open(Spec{Upstream: Anthropic, Model: "claude-opus-4-5", Secret: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := made.(*AnthropicModelProvider); !ok {
		t.Errorf("Anthropic reached %T", made)
	}
}
