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

package model

import (
	"io"
)

// DryRunPrefix is a special prefix that triggers model providers to estimate
// token count and price without actually calling the AI model APIs.
const DryRunPrefix = "$CloudDryRun$"

type ModelResult struct {
	PromptTokenCount   int
	ResponseTokenCount int
	TotalTokenCount    int
	ImageCount         int
	TotalPrice         float64
	Currency           string

	// CacheReadTokenCount and CacheWriteTokenCount are the prompt tokens a
	// provider served from, and wrote to, its prompt cache.
	//
	// They are part of PromptTokenCount, not additional to it — a provider
	// reports the cached portion of the prompt it already charged for, at a
	// different rate (a read is typically a tenth of the input price, a write
	// a quarter more). So they price the SAME tokens differently; adding them
	// to a total would count those tokens twice.
	//
	// Everything downstream of here was already built to carry them — the
	// usage record, the ClickHouse columns, the per-rate pricing, the gen_ai
	// span attributes. This struct was the one place they had nowhere to sit,
	// so every request reported zero reads and zero writes however well the
	// provider cached, and the saving was invisible to billing and to o11y
	// alike. Reading them is the whole fix.
	CacheReadTokenCount  int
	CacheWriteTokenCount int
}

func newModelResult(promptTokenCount int, responseTokenCount int, totalTokenCount int) *ModelResult {
	return &ModelResult{
		PromptTokenCount:   promptTokenCount,
		ResponseTokenCount: responseTokenCount,
		TotalTokenCount:    totalTokenCount,
	}
}

type ModelProvider interface {
	GetPricing() string
	QueryText(question string, writer io.Writer, history []*RawMessage, prompt string, knowledgeMessages []*RawMessage, agentInfo *AgentInfo, lang string) (*ModelResult, error)
}

// Upstream names what serves a call, never what is served: Anthropic serves
// Claude, OpenAI serves GPT. Some are third-party APIs, some are our own compute
// — Hanzo and Zen run on our GPUs — and some are a runner a deployment starts
// itself, like Ollama or Local. All of them answer the same question, which is
// the code that carries a request out.
//
// The set is closed and each member has one spelling, held by the compiler.
type Upstream string

const (
	AlibabaCloud  Upstream = "Alibaba Cloud"
	AmazonBedrock Upstream = "Amazon Bedrock"
	Anthropic     Upstream = "Anthropic"
	Azure         Upstream = "Azure"
	Baichuan      Upstream = "Baichuan"
	BaiduCloud    Upstream = "Baidu Cloud"
	ChatGLM       Upstream = "ChatGLM"
	Cohere        Upstream = "Cohere"
	DeepSeek      Upstream = "DeepSeek"
	DigitalOcean  Upstream = "DigitalOcean"
	Dummy         Upstream = "Dummy"
	Enso          Upstream = "Enso"
	Fireworks     Upstream = "Fireworks"
	Gemini        Upstream = "Gemini"
	GitHub        Upstream = "GitHub"
	Grok          Upstream = "Grok"
	Jina          Upstream = "Jina"
	Hanzo         Upstream = "Hanzo"
	HuggingFace   Upstream = "Hugging Face"
	IFlytek       Upstream = "iFlytek"
	Local         Upstream = "Local"
	MiniMax       Upstream = "MiniMax"
	Mistral       Upstream = "Mistral"
	Moonshot      Upstream = "Moonshot"
	Ollama        Upstream = "Ollama"
	OpenAI        Upstream = "OpenAI"
	OpenRouter    Upstream = "OpenRouter"
	SiliconFlow   Upstream = "Silicon Flow"
	StepFun       Upstream = "StepFun"
	TencentCloud  Upstream = "Tencent Cloud"
	VolcanoEngine Upstream = "Volcano Engine"
	Word2Vec      Upstream = "Word2Vec"
	Writer        Upstream = "Writer"
	Yi            Upstream = "Yi"
	Zen           Upstream = "Zen"
)

// Spec is what a provider row says about reaching its upstream: which upstream,
// which model, the credentials, and the dials.
type Spec struct {
	Upstream Upstream
	Model    string // the vendor's own name for it, e.g. claude-opus-4-5

	ClientID string
	Secret   string
	UserKey  string

	URL        string
	APIVersion string
	Compatible string // the API this upstream speaks when it is not its own

	Temperature float32
	TopP        float32
	TopK        int
	Frequency   float32
	Presence    float32
	Thinking    bool

	InputPrice  float64 // per thousand tokens
	OutputPrice float64 // per thousand tokens
	Currency    string
}

// upstreams answers, for each upstream, with the provider that speaks to it.
// Adding one is a row, and the key is the name.
var upstreams = map[Upstream]func(Spec) (ModelProvider, error){
	Ollama: func(s Spec) (ModelProvider, error) {
		return NewLocalModelProvider("Custom-think", "custom-model", "randomString", s.Temperature, s.TopP, 0, 0, s.URL, s.Model, s.InputPrice, s.OutputPrice, s.Currency)
	},
	Local: func(s Spec) (ModelProvider, error) {
		return NewLocalModelProvider(string(s.Upstream), s.Model, s.Secret, s.Temperature, s.TopP, s.Frequency, s.Presence, s.URL, s.Compatible, s.InputPrice, s.OutputPrice, s.Currency)
	},
	OpenAI: func(s Spec) (ModelProvider, error) {
		return NewOpenAiModelProvider(s.Model, s.Secret, s.URL, s.Temperature, s.TopP, s.Frequency, s.Presence)
	},
	DigitalOcean: func(s Spec) (ModelProvider, error) {
		return NewLocalModelProvider(string(s.Upstream), s.Model, s.Secret, s.Temperature, s.TopP, s.Frequency, s.Presence, s.URL, "", s.InputPrice, s.OutputPrice, s.Currency)
	},
	Fireworks: func(s Spec) (ModelProvider, error) {
		return NewFireworksProvider(s.Model, s.Secret, s.Temperature, s.TopP, s.Frequency, s.Presence)
	},
	Gemini: func(s Spec) (ModelProvider, error) {
		return NewGeminiModelProvider(s.Model, s.Secret, s.Temperature, s.TopP, s.TopK)
	},
	Azure: func(s Spec) (ModelProvider, error) {
		return NewAzureModelProvider(string(s.Upstream), s.Model, s.ClientID, s.Secret, s.Temperature, s.TopP, s.Frequency, s.Presence, s.URL, s.APIVersion)
	},
	HuggingFace: func(s Spec) (ModelProvider, error) {
		return NewHuggingFaceModelProvider(s.Model, s.Secret, s.Temperature)
	},
	Anthropic: func(s Spec) (ModelProvider, error) {
		return NewAnthropicModelProvider(s.Model, s.Secret, s.Thinking, s.TopK)
	},
	Grok: func(s Spec) (ModelProvider, error) {
		return NewGrokModelProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
	OpenRouter: func(s Spec) (ModelProvider, error) {
		return NewOpenRouterModelProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
	BaiduCloud: func(s Spec) (ModelProvider, error) {
		return NewBaiduCloudModelProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
	IFlytek: func(s Spec) (ModelProvider, error) { return NewiFlytekModelProvider(s.Model, s.Secret, s.Temperature) },
	ChatGLM: func(s Spec) (ModelProvider, error) { return NewChatGLMModelProvider(s.Model, s.Secret) },
	MiniMax: func(s Spec) (ModelProvider, error) {
		return NewMiniMaxModelProvider(s.Model, s.ClientID, s.Secret, s.Temperature)
	},
	Cohere: func(s Spec) (ModelProvider, error) { return NewCohereModelProvider(s.Model, s.Secret) },
	Moonshot: func(s Spec) (ModelProvider, error) {
		return NewMoonshotModelProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
	AmazonBedrock: func(s Spec) (ModelProvider, error) {
		return NewAmazonBedrockModelProvider(s.Model, s.Secret, float64(s.Temperature))
	},
	AlibabaCloud: func(s Spec) (ModelProvider, error) {
		return NewAlibabacloudModelProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
	Baichuan: func(s Spec) (ModelProvider, error) {
		return NewBaichuanModelProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
	VolcanoEngine: func(s Spec) (ModelProvider, error) {
		return NewVolcengineModelProvider(s.Model, s.URL, s.Secret, s.Temperature, s.TopP)
	},
	DeepSeek: func(s Spec) (ModelProvider, error) {
		return NewDeepSeekProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
	StepFun: func(s Spec) (ModelProvider, error) {
		return NewStepFunModelProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
	TencentCloud: func(s Spec) (ModelProvider, error) {
		return NewTencentCloudProvider(s.Secret, s.URL, s.Model, s.Temperature, s.TopP)
	},
	Mistral: func(s Spec) (ModelProvider, error) { return NewMistralProvider(s.Secret, s.Model) },
	Yi:      func(s Spec) (ModelProvider, error) { return NewYiProvider(s.Model, s.Secret, s.Temperature, s.TopP) },
	SiliconFlow: func(s Spec) (ModelProvider, error) {
		return NewSiliconFlowProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
	Zen: func(s Spec) (ModelProvider, error) {
		return NewLocalModelProvider(string(s.Upstream), s.Model, s.Secret, s.Temperature, s.TopP, s.Frequency, s.Presence, s.URL, "openai", s.InputPrice, s.OutputPrice, s.Currency)
	},
	Enso: func(s Spec) (ModelProvider, error) {
		return NewLocalModelProvider(string(s.Upstream), s.Model, s.Secret, s.Temperature, s.TopP, s.Frequency, s.Presence, s.URL, "openai", s.InputPrice, s.OutputPrice, s.Currency)
	},
	Hanzo: func(s Spec) (ModelProvider, error) {
		return NewLocalModelProvider(string(s.Upstream), s.Model, s.Secret, s.Temperature, s.TopP, s.Frequency, s.Presence, s.URL, "openai", s.InputPrice, s.OutputPrice, s.Currency)
	},
	Dummy: func(s Spec) (ModelProvider, error) { return NewDummyModelProvider(s.Model) },
	GitHub: func(s Spec) (ModelProvider, error) {
		return NewGitHubModelProvider(string(s.Upstream), s.Model, s.Secret, s.Temperature, s.TopP, s.Frequency, s.Presence)
	},
	Writer: func(s Spec) (ModelProvider, error) {
		return NewWriterModelProvider(s.Model, s.Secret, s.Temperature, s.TopP)
	},
}

// Open reaches the upstream a spec names.
//
// An upstream this build does not speak to answers (nil, nil) — absent, the same
// answer a row that is not there gives, which the caller reports as "the model
// provider type: %s is not supported". An error means the upstream is known and
// could not be reached.
func Open(s Spec) (ModelProvider, error) {
	reach, ok := upstreams[s.Upstream]
	if !ok {
		return nil, nil
	}
	return reach(s)
}
