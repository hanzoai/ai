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
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/proxy"
	"github.com/hanzoai/go-openai"
)

type LocalModelProvider struct {
	typ                          string
	subType                      string
	deploymentName               string
	secretKey                    string
	temperature                  float32
	topP                         float32
	frequencyPenalty             float32
	presencePenalty              float32
	providerUrl                  string
	apiVersion                   string
	compatibleProvider           string
	inputPricePerThousandTokens  float64
	outputPricePerThousandTokens float64
	currency                     string
}

// answerVia asks a provider that speaks the OpenAI dialect and prices what came
// back.
//
// Nine providers reach their upstream this way: build a local provider pointed at
// the upstream's base URL, ask it, then price the answer against that provider's
// own table. What differs between them is the URL, the key and the dialect; what
// did not differ was this — ask, stop on a refusal, price, stop on a refusal —
// written out nine times.
func answerVia(local ModelProvider, price func(*ModelResult, string) error,
	question string, writer io.Writer, history []*RawMessage, prompt string,
	knowledgeMessages []*RawMessage, agentInfo *AgentInfo, lang string) (*ModelResult, error) {
	result, err := local.QueryText(question, writer, history, prompt, knowledgeMessages, agentInfo, lang)
	if err != nil {
		return nil, err
	}
	if err := price(result, lang); err != nil {
		return nil, err
	}
	return result, nil
}

func NewLocalModelProvider(typ string, subType string, secretKey string, temperature float32, topP float32, frequencyPenalty float32, presencePenalty float32, providerUrl string, compatibleProvider string, inputPricePerThousandTokens float64, outputPricePerThousandTokens float64, Currency string) (*LocalModelProvider, error) {
	p := &LocalModelProvider{
		typ:                          typ,
		subType:                      subType,
		secretKey:                    secretKey,
		temperature:                  temperature,
		topP:                         topP,
		frequencyPenalty:             frequencyPenalty,
		presencePenalty:              presencePenalty,
		providerUrl:                  providerUrl,
		compatibleProvider:           compatibleProvider,
		inputPricePerThousandTokens:  inputPricePerThousandTokens,
		outputPricePerThousandTokens: outputPricePerThousandTokens,
		currency:                     Currency,
	}
	return p, nil
}

// getLocalClientFromUrl builds a client for an OpenAI-shaped endpoint, and
// VERIFIES the server's certificate.
//
// The name records the intent — a model server on this machine, whose
// certificate is self-signed because nobody issues one for 127.0.0.1 — and the
// exemption for that case is kept below. But the same client is handed to
// DigitalOcean, Custom and Custom-think, which are vendors reached across the
// internet, and it carries an API key to each of them. A connection that accepts
// any certificate hands that key to whoever presents one, and the call still
// succeeds, so nothing downstream can notice.
//
// Verification is therefore the default and the exemption is narrowed to
// loopback, where no attacker can stand between the two ends.
func getLocalClientFromUrl(authToken string, url string) *openai.Client {
	config := openai.DefaultConfig(authToken)
	config.BaseURL = url

	if hc := proxy.Local(url); hc != nil {
		config.HTTPClient = hc
	}

	c := openai.NewClientWithConfig(config)
	return c
}

func (p *LocalModelProvider) GetPricing() string {
	return getOpenAIModelPrice()
}

func (p *LocalModelProvider) CalculatePrice(modelResult *ModelResult, lang string) error {
	// Use provider-configured pricing for custom models and DigitalOcean
	if p.subType == "custom-model" || p.typ == "DigitalOcean" || p.inputPricePerThousandTokens > 0 {
		inputPrice := getPrice(modelResult.PromptTokenCount, p.inputPricePerThousandTokens)
		outputPrice := getPrice(modelResult.ResponseTokenCount, p.outputPricePerThousandTokens)
		modelResult.TotalPrice = AddPrices(inputPrice, outputPrice)
		modelResult.Currency = p.currency
		if modelResult.Currency == "" {
			modelResult.Currency = "USD"
		}
		return nil
	}
	return CalculateOpenAIModelPrice(p.subType, modelResult, lang)
}

func flushDataAzure(data string, writer io.Writer, lang string) error {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("%s", i18n.Translate(lang, "model:writer does not implement http.Flusher"))
	}
	for _, runeValue := range data {
		char := string(runeValue)
		_, err := fmt.Fprintf(writer, "event: message\ndata: %s\n\n", char)
		if err != nil {
			return err
		}

		flusher.Flush()

		delta := 0
		if !unicode.In(runeValue, unicode.Latin) {
			delta = 50
		}

		var delay int
		if char == "," || char == "，" {
			delay = 20 + rand.Intn(50) + delta
		} else if char == "." || char == "。" || char == "!" || char == "！" || char == "?" || char == "？" {
			delay = 50 + rand.Intn(50) + delta
		} else if char == " " || char == "　" || char == "(" || char == "（" || char == ")" || char == "）" {
			delay = 10 + rand.Intn(50) + delta
		} else {
			delay = rand.Intn(1 + delta*2/5)
		}

		if unicode.In(runeValue, unicode.Latin) {
			delay -= 20
		}

		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
	}
	return nil
}

func flushDataOpenai(data string, writer io.Writer, lang string) error {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("%s", i18n.Translate(lang, "model:writer does not implement http.Flusher"))
	}
	if _, err := fmt.Fprintf(writer, "event: message\ndata: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func flushDataThink(data string, eventType string, writer io.Writer, lang string) error {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return fmt.Errorf("%s", i18n.Translate(lang, "model:writer does not implement http.Flusher"))
	}
	if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", eventType, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// dialect answers how this provider reaches its upstream and how it writes an
// answer back, decided once from its type.
//
// Exactly one of the two flushes is set. flushThink takes each piece's kind
// ("reason" or "message"), so a reasoning model's thinking streams too;
// flushStandard writes the answer only. Both used to be one `any`, asserted to
// one shape or the other by a second test of p.typ in each loop. When the two
// tests disagreed, the assertion panicked: a Local or DigitalOcean completion
// model met the standard assertion holding flushDataThink. With typed values
// there is nothing to assert.
//
// A type not named here gets no client. Zen, Enso and Hanzo were missing (Open
// builds them as LocalModelProviders of that type), and so was DeepSeek with a
// model other than chat or reasoner, which arrives with no type at all. Each
// reached client.CreateChatCompletionStream on a nil client and panicked;
// QueryText now refuses an absent client instead.
func (p *LocalModelProvider) dialect() (client *openai.Client, flushThink func(string, string, io.Writer, string) error, flushStandard func(string, io.Writer, string) error) {
	switch p.typ {
	case "Local", "DigitalOcean", "Custom-think":
		return getLocalClientFromUrl(p.secretKey, p.providerUrl), flushDataThink, nil
	case "Zen", "Enso", "Hanzo":
		// Hanzo's own upstreams speak the OpenAI dialect and may stream reasoning;
		// the think flush writes the answer as the same "message" events the
		// standard one does, and the reasoning as "reason" instead of dropping it.
		return getLocalClientFromUrl(p.secretKey, p.providerUrl), flushDataThink, nil
	case "Azure":
		return getAzureClientFromToken(p.deploymentName, p.secretKey, p.providerUrl, p.apiVersion), nil, flushDataAzure
	case "GitHub":
		return getGitHubClientFromToken(p.secretKey, p.providerUrl), nil, flushDataOpenai
	case "Custom":
		return getLocalClientFromUrl(p.secretKey, p.providerUrl), nil, flushDataOpenai
	}
	return nil, nil, nil
}

func (p *LocalModelProvider) QueryText(question string, writer io.Writer, history []*RawMessage, prompt string, knowledgeMessages []*RawMessage, agentInfo *AgentInfo, lang string) (*ModelResult, error) {
	client, flushThink, flushStandard := p.dialect()
	if client == nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "model:QueryText() error: unknown provider type: %s"), p.typ))
	}

	ctx := context.Background()
	flusher, ok := writer.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("%s", i18n.Translate(lang, "model:writer does not implement http.Flusher"))
	}

	model := p.subType
	if model == "custom-model" && p.compatibleProvider != "" {
		model = p.compatibleProvider
	} else if model == "custom-model" && p.compatibleProvider == "" {
		model = "gpt-3.5-turbo"
	}

	temperature := p.temperature
	topP := p.topP
	frequencyPenalty := p.frequencyPenalty
	presencePenalty := p.presencePenalty

	maxTokens := GetContextLength(model)

	modelResult := &ModelResult{}
	if getOpenAiModelType(p.subType) == "Chat" {
		rawMessages, err := OpenaiGenerateMessages(prompt, question, history, knowledgeMessages, model, maxTokens, lang)
		if err != nil {
			return nil, err
		}
		if agentInfo != nil && agentInfo.AgentMessages != nil && agentInfo.AgentMessages.Messages != nil {
			rawMessages = append(rawMessages, agentInfo.AgentMessages.Messages...)
		}

		var messages []openai.ChatCompletionMessage
		if IsVisionModel(p.subType) {
			messages = OpenaiRawMessagesToGptVisionMessages(rawMessages)
		} else {
			messages = OpenaiRawMessagesToMessages(rawMessages)
		}

		// https://github.com/hanzoai/go-openai/pull/223#issuecomment-1494372875
		promptTokenCount, err := OpenaiNumTokensFromMessages(messages, model)
		if err != nil {
			return nil, err
		}

		modelResult.PromptTokenCount = promptTokenCount
		modelResult.TotalTokenCount = modelResult.PromptTokenCount + modelResult.ResponseTokenCount
		err = p.CalculatePrice(modelResult, lang)
		if err != nil {
			return nil, err
		}

		if strings.HasPrefix(question, "$CloudDryRun$") {
			return modelResult, nil

			//if GetOpenAiMaxTokens(p.subType) > modelResult.TotalTokenCount {
			//	return modelResult, nil
			//} else {
			//	return nil, fmt.Errorf(i18n.Translate(lang, "model:exceed max tokens"))
			//}
		}

		req := ChatCompletionRequest(model, messages, temperature, topP, frequencyPenalty, presencePenalty)
		if agentInfo != nil && agentInfo.AgentClients != nil {
			tools, err := reverseToolsToOpenAi(agentInfo.AgentClients.Tools)
			if err != nil {
				return nil, err
			}
			req.Tools = tools
			req.ToolChoice = "auto"
		}

		respStream, err := client.CreateChatCompletionStream(
			ctx,
			req,
		)
		if err != nil {
			return nil, err
		}
		defer respStream.Close()

		isLeadingReturn := true
		var (
			answerData   strings.Builder
			toolCalls    []openai.ToolCall
			toolCallsMap map[int]int
		)

		for {
			completion, streamErr := respStream.Recv()
			if streamErr != nil {
				if streamErr == io.EOF {
					break
				}
				return nil, streamErr
			}

			if len(completion.Choices) == 0 {
				continue
			}
			if completion.Choices[0].Delta.ToolCalls != nil {
				for _, toolCall := range completion.Choices[0].Delta.ToolCalls {
					toolCalls, toolCallsMap = handleToolCallsParameters(toolCall, toolCalls, toolCallsMap)
				}
			}

			// Handle both regular content and reasoning content
			if flushThink != nil {
				// Check if we have reasoning content (think_content)
				if completion.Choices[0].Delta.ReasoningContent != "" {
					reasoningData := completion.Choices[0].Delta.ReasoningContent
					err = flushThink(reasoningData, "reason", writer, lang)
					if err != nil {
						return nil, err
					}
				}

				// Handle regular content
				if completion.Choices[0].Delta.Content != "" {
					data := completion.Choices[0].Delta.Content
					if isLeadingReturn && len(data) != 0 {
						if strings.Count(data, "\n") == len(data) {
							continue
						} else {
							isLeadingReturn = false
						}
					}

					err = flushThink(data, "message", writer, lang)
					if err != nil {
						return nil, err
					}

					answerData.WriteString(data)
				}
			} else {
				// For all other provider types, use the standard flush function
				data := completion.Choices[0].Delta.Content
				if isLeadingReturn && len(data) != 0 {
					if strings.Count(data, "\n") == len(data) {
						continue
					} else {
						isLeadingReturn = false
					}
				}

				err = flushStandard(data, writer, lang)
				if err != nil {
					return nil, err
				}

				answerData.WriteString(data)
			}
		}

		if agentInfo != nil && agentInfo.AgentMessages != nil {
			agentInfo.AgentMessages.ToolCalls = toolCalls
		}

		// https://github.com/hanzoai/go-openai/pull/223#issuecomment-1494372875
		responseTokenCount, err := GetTokenSize(model, answerData.String())
		if err != nil {
			return nil, err
		}

		modelResult.ResponseTokenCount += responseTokenCount
		modelResult.TotalTokenCount = modelResult.PromptTokenCount + modelResult.ResponseTokenCount
		err = p.CalculatePrice(modelResult, lang)
		if err != nil {
			return nil, err
		}
		return modelResult, nil
	} else if getOpenAiModelType(p.subType) == "imagesGenerations" {
		if strings.HasPrefix(question, "$CloudDryRun$") {
			return modelResult, nil
		}
		reqUrl := openai.ImageRequest{
			Prompt:         question,
			Model:          openai.CreateImageModelDallE3,
			Size:           openai.CreateImageSize1024x1024,
			ResponseFormat: openai.CreateImageResponseFormatURL,
			N:              1,
		}

		respUrl, err := client.CreateImage(ctx, reqUrl)
		if err != nil {
			return nil, err
		}

		url := fmt.Sprintf("<img src=\"%s\" width=\"100%%\" height=\"auto\">", respUrl.Data[0].URL)
		fmt.Fprint(writer, url)
		flusher.Flush()

		modelResult.ImageCount = 1
		modelResult.TotalTokenCount = modelResult.ImageCount
		err = p.CalculatePrice(modelResult, lang)
		if err != nil {
			return nil, err
		}
		return modelResult, nil
	} else if getOpenAiModelType(p.subType) == "Completion" {
		respStream, err := client.CreateCompletionStream(
			ctx,
			openai.CompletionRequest{
				Model:            model,
				Prompt:           question,
				Stream:           true,
				Temperature:      temperature,
				TopP:             topP,
				FrequencyPenalty: frequencyPenalty,
				PresencePenalty:  presencePenalty,
			},
		)
		if err != nil {
			return nil, err
		}
		defer respStream.Close()

		isLeadingReturn := true
		var response strings.Builder
		for {
			completion, streamErr := respStream.Recv()
			if streamErr != nil {
				if streamErr == io.EOF {
					break
				}
				return nil, streamErr
			}

			// A chunk can carry no choice (a usage-only chunk does); the chat loop
			// above already skips one, and indexing it here panicked.
			if len(completion.Choices) == 0 {
				continue
			}
			data := completion.Choices[0].Text
			if isLeadingReturn && len(data) != 0 {
				if strings.Count(data, "\n") == len(data) {
					continue
				} else {
					isLeadingReturn = false
				}
			}

			if flushThink != nil {
				err = flushThink(data, "message", writer, lang)
			} else {
				err = flushStandard(data, writer, lang)
			}

			if err != nil {
				return nil, err
			}

			_, err = response.WriteString(data)
			if err != nil {
				return nil, err
			}
		}

		// A count that failed is an error, not a nil result with no error: the
		// caller prices what comes back and would dereference the nil.
		modelResult, err = getDefaultModelResult(model, question, response.String())
		if err != nil {
			return nil, err
		}
		return modelResult, nil
	} else {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "model:QueryText() error: unknown model type: %s"), p.subType))
	}
}
