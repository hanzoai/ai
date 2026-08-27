// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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
	"context"

	"github.com/hanzoai/ai/model"
)

type EmbeddingResult struct {
	TokenCount int
	Price      float64
	Currency   string
}

type EmbeddingProvider interface {
	GetPricing() string
	QueryVector(text string, ctx context.Context, lang string) ([]float32, *EmbeddingResult, error)
}

// Spec is what a provider row says about reaching its vendor for embeddings. The
// vendor names are model.Vendor because a vendor is a company, not a category:
// OpenAI sells models and embeddings and is one name for one company.
type Spec struct {
	Vendor model.Vendor
	Model  string

	ClientID string
	Secret   string

	URL        string
	APIVersion string

	Price    float64 // per thousand tokens
	Currency string
	Lang     string
}

// vendors answers, for each vendor, with the embedding provider that reaches it.
var vendors = map[model.Vendor]func(Spec) (EmbeddingProvider, error){
	model.OpenAI: func(s Spec) (EmbeddingProvider, error) {
		return NewOpenAiEmbeddingProvider(string(s.Vendor), s.Model, s.Secret)
	},
	model.Gemini:      func(s Spec) (EmbeddingProvider, error) { return NewGeminiEmbeddingProvider(s.Model, s.Secret) },
	model.HuggingFace: func(s Spec) (EmbeddingProvider, error) { return NewHuggingFaceEmbeddingProvider(s.Model, s.Secret) },
	model.Cohere: func(s Spec) (EmbeddingProvider, error) {
		return NewCohereEmbeddingProvider(s.Model, s.ClientID, s.Secret)
	},
	model.BaiduCloud: func(s Spec) (EmbeddingProvider, error) {
		return NewBaiduCloudEmbeddingProvider(s.Model, s.ClientID, s.Secret)
	},
	model.Ollama: func(s Spec) (EmbeddingProvider, error) {
		return NewLocalEmbeddingProvider("Custom", "custom-embedding", "randomString", s.URL, s.Model, s.Price, s.Currency)
	},
	model.Local: func(s Spec) (EmbeddingProvider, error) {
		return NewLocalEmbeddingProvider(string(s.Vendor), s.Model, s.Secret, s.URL, s.Model, s.Price, s.Currency)
	},
	model.Azure: func(s Spec) (EmbeddingProvider, error) {
		return NewAzureEmbeddingProvider(string(s.Vendor), s.Model, s.ClientID, s.Secret, s.URL, s.APIVersion)
	},
	model.MiniMax: func(s Spec) (EmbeddingProvider, error) {
		return NewMiniMaxEmbeddingProvider(string(s.Vendor), s.Model, s.Secret, s.URL)
	},
	model.AlibabaCloud: func(s Spec) (EmbeddingProvider, error) {
		return NewAlibabacloudEmbeddingProvider(string(s.Vendor), s.Model, s.Secret, s.URL)
	},
	model.TencentCloud: func(s Spec) (EmbeddingProvider, error) {
		return NewTencentCloudEmbeddingProvider(s.ClientID, s.Secret, s.Lang)
	},
	model.Jina: func(s Spec) (EmbeddingProvider, error) { return NewJinaEmbeddingProvider(s.Model, s.Secret) },
	model.Word2Vec: func(s Spec) (EmbeddingProvider, error) {
		return NewWord2VecEmbeddingProvider(string(s.Vendor), s.Model, s.Lang)
	},
	model.Dummy: func(s Spec) (EmbeddingProvider, error) { return NewDummyEmbeddingProvider(s.Model) },
}

// Open reaches the vendor a spec names, answering (nil, nil) for one this build
// does not speak to — the same absence the model side gives.
func Open(s Spec) (EmbeddingProvider, error) {
	reach, ok := vendors[s.Vendor]
	if !ok {
		return nil, nil
	}
	return reach(s)
}

func GetDefaultEmbeddingResult(modelSubType string, text string) (*EmbeddingResult, error) {
	tokenCount, err := model.GetTokenSize(modelSubType, text)
	if err != nil {
		tokenCount, err = model.GetTokenSize("text-embedding-ada-002", text)
	}
	if err != nil {
		return nil, err
	}

	price := getPrice(tokenCount, 0.0001)
	currency := "USD"

	res := &EmbeddingResult{
		TokenCount: tokenCount,
		Price:      price,
		Currency:   currency,
	}
	return res, nil
}
