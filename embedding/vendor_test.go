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

// The vendors here are model.Vendor because a vendor is a company, not a
// category: OpenAI sells models and embeddings under one name. This table serves
// the ones that sell embeddings, which includes two — Jina, Word2Vec — that sell
// no model, and that is why a name is not required to answer in both places.
func TestEmbeddingVendorsAreTheSameNames(t *testing.T) {
	for _, v := range []model.Vendor{model.OpenAI, model.Gemini, model.Cohere, model.Jina, model.Word2Vec} {
		if _, ok := vendors[v]; !ok {
			t.Errorf("no embedding provider answers to %q", v)
		}
	}
	if len(vendors) == 0 {
		t.Fatal("no vendors — this check is reading nothing")
	}
}

// A vendor this build does not speak to is absent, not an error — the same answer
// the model side gives, which the caller turns into "not supported".
func TestAnUnknownEmbeddingVendorIsAbsent(t *testing.T) {
	made, err := Open(Spec{Vendor: "Nobody", Model: "m"})
	if err != nil {
		t.Errorf("an unknown vendor answered with an error: %v", err)
	}
	if made != nil {
		t.Errorf("an unknown vendor answered with a provider: %#v", made)
	}
}
