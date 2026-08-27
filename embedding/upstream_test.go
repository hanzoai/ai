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

package embedding

import (
	"testing"

	"github.com/hanzoai/ai/model"
)

// The upstreams here are model.Upstream because a vendor is a company, not a
// category: OpenAI sells models and embeddings under one name. This table serves
// the ones that sell embeddings, which includes two — Jina, Word2Vec — that sell
// no model, and that is why a name is not required to answer in both places.
func TestEmbeddingVendorsAreTheSameNames(t *testing.T) {
	for _, v := range []model.Upstream{model.OpenAI, model.Gemini, model.Cohere, model.Jina, model.Word2Vec} {
		if _, ok := upstreams[v]; !ok {
			t.Errorf("no embedding provider answers to %q", v)
		}
	}
	if len(upstreams) == 0 {
		t.Fatal("no upstreams — this check is reading nothing")
	}
}

// An upstream this build does not speak to is absent, not an error — the answer
// the model side gives.
func TestAnUnknownEmbeddingUpstreamIsAbsent(t *testing.T) {
	made, err := Open(Spec{Upstream: "Nobody", Model: "m"})
	if err != nil {
		t.Errorf("an unknown upstream answered with an error: %v", err)
	}
	if made != nil {
		t.Errorf("an unknown upstream answered with a provider: %#v", made)
	}
}
