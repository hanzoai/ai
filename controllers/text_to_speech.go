// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

package controllers

import (
	"encoding/json"
	"time"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/tts"
	"github.com/hanzoai/ai/util"
)

type TextToSpeechRequest struct {
	StoreId    string `json:"storeId"`
	ProviderId string `json:"providerId"`
	MessageId  string `json:"messageId"`
	Text       string `json:"text"`
}

// GenerateTextToSpeechAudio
// @Title GenerateTextToSpeechAudio
// @Tag TTS API
// @Description convert text to speech
// @Param body controllers.TextToSpeechRequest true "The text to convert to speech"
// @Success 200 {object} []byte The audio data
// @router /generate-text-to-speech-audio [post]
func (c *ApiController) GenerateTextToSpeechAudio() {
	var req TextToSpeechRequest
	err := json.Unmarshal(c.Ctx.Input.RequestBody, &req)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	message, chat, providerObj, ctx, err := object.PrepareTextToSpeech(req.StoreId, req.ProviderId, req.MessageId, req.Text, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	startTime := time.Now().UTC()
	audioData, ttsResult, err := providerObj.QueryAudio(message.Text, ctx, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if audioData == nil {
		c.ResponseError(c.T("tts:The audio data is nil"))
		return
	}

	err = object.UpdateChatStats(chat, ttsResult)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.recordLegacyTTSUsage(chat, req.ProviderId, ttsResult, startTime)

	c.ResponseAudio(audioData, "audio/mp3", "speech.mp3")
}

// GenerateTextToSpeechAudioStream
// @Title GenerateTextToSpeechAudioStream
// @Tag TTS API
// @Description convert text to speech with streaming
// @Param storeId query string true "The store ID"
// @Param messageId query string true "The message ID"
// @Success 200 {stream} string "An event stream of audio chunks in base64 format"
// @router /generate-text-to-speech-audio-stream [get]
func (c *ApiController) GenerateTextToSpeechAudioStream() {
	storeId := c.Input().Get("storeId")
	messageId := c.Input().Get("messageId")

	c.Ctx.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
	c.Ctx.ResponseWriter.Header().Set("Cache-Control", "no-cache")
	c.Ctx.ResponseWriter.Header().Set("Connection", "keep-alive")

	message, chat, providerObj, ctx, err := object.PrepareTextToSpeech(storeId, "", messageId, "", c.GetAcceptLanguage())
	if err != nil {
		c.ResponseErrorStream(message, err.Error())
		return
	}

	startTime := time.Now().UTC()
	ttsResult, err := providerObj.QueryAudioStream(message.Text, ctx, c.Ctx.ResponseWriter, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseErrorStream(message, err.Error())
		return
	}

	err = object.UpdateChatStats(chat, ttsResult)
	if err != nil {
		log.Error("Error updating chat: %s", err.Error())
	}
	c.recordLegacyTTSUsage(chat, "", ttsResult, startTime)
}

// recordLegacyTTSUsage meters the legacy store-bound TTS handlers into the one
// usage/o11y plane. The legacy lane accumulates a possibly non-USD float price
// onto chat.Price (UpdateChatStats) and never debited commerce, so: the trace
// (warehouse row + gen_ai span) is ALWAYS emitted, and recordUsage runs only
// for a USD-priced result (a CNY float must not be debited as dollars — that
// currency fold is the legacy-price rip, tracked separately).
func (c *ApiController) recordLegacyTTSUsage(chat *object.Chat, providerId string, ttsResult *tts.TextToSpeechResult, startTime time.Time) {
	if chat == nil || ttsResult == nil {
		return
	}
	org := chat.Organization
	if org == "" {
		org = chat.Owner
	}
	model := providerId
	if model == "" {
		model = chat.ModelProvider
	}
	if model == "" {
		model = "tts"
	}
	rec := &usageRecord{
		Owner:        org,
		Organization: org,
		User:         chat.User,
		Model:        model,
		Provider:     model,
		TotalTokens:  ttsResult.TokenCount,
		Cost:         ttsResult.Price,
		Currency:     ttsResult.Currency,
		Status:       "success",
		ClientIP:     c.Ctx.Request.RemoteAddr,
		RequestID:    util.GenerateUUID(),
	}
	if rec.Currency == "" || rec.Currency == "USD" {
		rec.Currency = "USD"
		recordUsage(rec)
	}
	recordTrace(c.Ctx.Request.Context(), rec, startTime)
}
