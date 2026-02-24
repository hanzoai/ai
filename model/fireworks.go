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
		// Fireworks pricing per 1K tokens (Feb 2026, from fireworks.ai/pricing)
		"accounts/fireworks/models/glm-5":                  {0.001, 0.0032},    // $1.00/$3.20 per MTok
		"accounts/fireworks/models/glm-4p7":                {0.0006, 0.0022},   // $0.60/$2.20 per MTok
		"accounts/fireworks/models/deepseek-v3p1":          {0.00056, 0.00168}, // $0.56/$1.68 per MTok
		"accounts/fireworks/models/deepseek-v3p2":          {0.00056, 0.00168}, // $0.56/$1.68 per MTok
		"accounts/fireworks/models/kimi-k2-instruct-0905":  {0.0006, 0.0025},   // $0.60/$2.50 per MTok
		"accounts/fireworks/models/kimi-k2-thinking":       {0.0006, 0.0025},   // $0.60/$2.50 per MTok
		"accounts/fireworks/models/kimi-k2p5":              {0.0006, 0.003},    // $0.60/$3.00 per MTok
		"accounts/fireworks/models/minimax-m2p1":           {0.0003, 0.0012},   // $0.30/$1.20 per MTok
		"accounts/fireworks/models/minimax-m2p5":           {0.0003, 0.0012},   // $0.30/$1.20 per MTok
		"accounts/cogito/models/cogito-671b-v2-p1":         {0.0012, 0.0012},   // $1.20/$1.20 per MTok
		"accounts/fireworks/models/gpt-oss-120b":           {0.00015, 0.0006},  // $0.15/$0.60 per MTok
		"accounts/fireworks/models/gpt-oss-20b":            {0.00007, 0.0003},  // $0.07/$0.30 per MTok
		"accounts/fireworks/models/mixtral-8x22b-instruct": {0.0009, 0.0009},   // $0.90/$0.90 per MTok
		"accounts/fireworks/models/qwen3-8b":               {0.0002, 0.0002},   // $0.20/$0.20 per MTok
		"accounts/fireworks/models/qwen3-vl-30b-a3b-instruct": {0.00015, 0.0006}, // $0.15/$0.60 per MTok
		"accounts/fireworks/models/qwen3-vl-30b-a3b-thinking": {0.00015, 0.0006}, // $0.15/$0.60 per MTok
		"accounts/fireworks/models/llama-v3p3-70b-instruct": {0.0009, 0.0009},  // $0.90/$0.90 per MTok
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
