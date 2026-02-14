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

import (
	"io"
)

type FireworksModelProvider struct {
	subType          string
	apiKey           string
	temperature      float32
	topP             float32
	frequencyPenalty float32
	presencePenalty  float32
}

func NewFireworksProvider(subType string, apiKey string, temperature float32, topP float32, frequencyPenalty float32, presencePenalty float32) (*FireworksModelProvider, error) {
	return &FireworksModelProvider{
		subType:          subType,
		apiKey:           apiKey,
		temperature:      temperature,
		topP:             topP,
		frequencyPenalty: frequencyPenalty,
		presencePenalty:  presencePenalty,
	}, nil
}

func (p *FireworksModelProvider) GetPricing() string {
	return `URL: https://fireworks.ai/pricing
| Model | Input Price per 1K tokens | Output Price per 1K tokens |
|---|---|---|
| accounts/fireworks/models/llama-v3p3-70b-instruct | $0.0009 | $0.0009 |
| accounts/fireworks/models/llama-v3p1-405b-instruct | $0.003 | $0.003 |
| accounts/fireworks/models/qwen3-235b-a22b | $0.0012 | $0.0048 |
| accounts/fireworks/models/deepseek-v3 | $0.0009 | $0.0009 |
| accounts/fireworks/models/mixtral-8x22b-instruct | $0.0012 | $0.0012 |`
}

func (p *FireworksModelProvider) calculatePrice(modelResult *ModelResult) error {
	priceTable := map[string][2]float64{
		"accounts/fireworks/models/llama-v3p3-70b-instruct":  {0.0009, 0.0009},
		"accounts/fireworks/models/llama-v3p1-405b-instruct": {0.003, 0.003},
		"accounts/fireworks/models/qwen3-235b-a22b":          {0.0012, 0.0048},
		"accounts/fireworks/models/deepseek-v3":              {0.0009, 0.0009},
		"accounts/fireworks/models/mixtral-8x22b-instruct":   {0.0012, 0.0012},
	}

	if prices, ok := priceTable[p.subType]; ok {
		inputPrice := getPrice(modelResult.PromptTokenCount, prices[0])
		outputPrice := getPrice(modelResult.ResponseTokenCount, prices[1])
		modelResult.TotalPrice = AddPrices(inputPrice, outputPrice)
		modelResult.Currency = "USD"
	}
	return nil
}

func (p *FireworksModelProvider) QueryText(question string, writer io.Writer, history []*RawMessage, prompt string, knowledgeMessages []*RawMessage, agentInfo *AgentInfo, lang string) (*ModelResult, error) {
	localProvider, err := NewLocalModelProvider(
		"Custom-think", "custom-model", p.apiKey,
		p.temperature, p.topP, p.frequencyPenalty, p.presencePenalty,
		"https://api.fireworks.ai/inference/v1", p.subType,
		0, 0, "USD",
	)
	if err != nil {
		return nil, err
	}

	modelResult, err := localProvider.QueryText(question, writer, history, prompt, knowledgeMessages, agentInfo, lang)
	if err != nil {
		return nil, err
	}

	err = p.calculatePrice(modelResult)
	if err != nil {
		return nil, err
	}

	return modelResult, nil
}
