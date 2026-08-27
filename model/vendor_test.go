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

// A vendor is named once and answered somewhere. The names are shared across
// categories — OpenAI sells models and embeddings and is one company — so a name
// need not be reachable HERE: Jina and Word2Vec are embedding vendors and this
// table is models. What must hold is the other direction. Every row in this table
// is a NAMED vendor, so no row can be keyed by a spelling the constants do not
// hold, which is the drift the type exists to stop.
func TestEveryRowIsANamedVendor(t *testing.T) {
	named := map[Vendor]bool{}
	for _, v := range []Vendor{
		AlibabaCloud, AmazonBedrock, Anthropic, Azure, Baichuan, BaiduCloud,
		ChatGLM, Cohere, DeepSeek, DigitalOcean, Dummy, Fireworks, Gemini,
		GitHub, Grok, Hanzo, HuggingFace, IFlytek, Jina, Local, MiniMax,
		Mistral, Moonshot, Ollama, OpenAI, OpenRouter, SiliconFlow, StepFun,
		TencentCloud, VolcanoEngine, Word2Vec, Writer, Yi, Zen,
	} {
		if named[v] {
			t.Errorf("vendor %q is named twice", v)
		}
		named[v] = true
	}
	for v := range vendors {
		if !named[v] {
			t.Errorf("the table is keyed by %q, which is not a named vendor", v)
		}
	}
	if len(vendors) == 0 {
		t.Fatal("no vendors — this check is reading nothing")
	}
}

// A vendor this build does not speak to is ABSENT, not an error — the same answer
// a row that is not there gives, which the caller turns into "not supported".
func TestAnUnknownVendorIsAbsentRatherThanAnError(t *testing.T) {
	made, err := Open(Spec{Vendor: "Nobody", Model: "m"})
	if err != nil {
		t.Errorf("an unknown vendor answered with an error: %v", err)
	}
	if made != nil {
		t.Errorf("an unknown vendor answered with a provider: %#v", made)
	}
}

// And the vendor that started this: the row says the company, not the model.
func TestAnthropicIsTheVendorNotTheModel(t *testing.T) {
	if Anthropic != "Anthropic" {
		t.Errorf("the vendor is spelled %q", Anthropic)
	}
	made, err := Open(Spec{Vendor: Anthropic, Model: "claude-opus-4-5", Secret: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := made.(*AnthropicModelProvider); !ok {
		t.Errorf("Anthropic reached %T", made)
	}
}
