// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/ai/object"
)

// ProcessSpeechToText
// @Title ProcessSpeechToText
// @Tag STT API
// @Description convert speech to text
// @Param audio formData file true "The audio file to convert to text"
// @Param storeId formData string true "The store ID"
// @Success 200 {object} controllers.SpeechToTextResponse The transcribed text
// @router /process-speech-to-text [post]
func (c *ApiController) ProcessSpeechToText() {
	// Read the audio part FIRST: doing so parses the multipart body, and until
	// something does, the router resolves GetString against an r.Form that Go has not
	// filled yet — so storeId, a form field here, read empty and this handler
	// refused every request that carried one. Same ordering hazard the OpenAI
	// transcription endpoint carried; see readTranscribeRequest.
	audioFile, _, err := c.GetFile("audio")
	if err != nil {
		c.ResponseError(fmt.Sprintf("Error getting audio audioFile: %s", err.Error()))
		return
	}
	defer audioFile.Close()

	storeId := c.GetString("storeId")
	if storeId == "" {
		c.ResponseError(c.T("stt:Missing required parameter: storeId"))
		return
	}

	store, err := object.GetStore(storeId)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if store == nil {
		c.ResponseError(fmt.Sprintf("The store: %s is not found", storeId))
		return
	}

	// Get STT provider
	provider, err := store.GetSpeechToTextProvider()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if provider == nil {
		c.ResponseError(fmt.Sprintf("The speech-to-text provider for store: %s is not found", store.GetId()))
		return
	}

	// Get provider implementation
	providerObj, err := provider.GetSpeechToTextProvider(c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// The same per-org ceiling the OpenAI-shaped door takes (speech_admission.go),
	// keyed on the org the CALLER acts in rather than the store owner the record
	// bills: the caller names the store, and capacity is refused on what the
	// credential proves rather than on what the body asks for.
	release, refused := admitSpeech(c.GetOrg())
	if refused != nil {
		c.ResponseErrorWithStatus(statusOf(refused), refused.Error())
		return
	}
	defer release()

	// Process the audio data and get the transcription
	ctx := context.Background()
	startTime := time.Now().UTC()
	text, sttResult, err := providerObj.ProcessAudio(audioFile, ctx, c.GetAcceptLanguage())
	c.recordLegacySTTUsage(store, provider, sttSecondsOf(sttResult), startTime, err)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Return the transcribed text
	c.ResponseOk(text)
}

// recordLegacySTTUsage traces the legacy store-bound STT handler into the one
// usage/o11y plane, carrying the SAME audio seconds the OpenAI-shaped path
// meters — so legacy traffic is measured, not merely counted, and prices itself
// the moment a rate exists. Errors stay visible: recordTrace is unconditional.
func (c *ApiController) recordLegacySTTUsage(store *object.Store, provider *object.Provider, seconds float64, startTime time.Time, callErr error) {
	if store == nil || provider == nil {
		return
	}
	status, errMsg := "success", ""
	if callErr != nil {
		status, errMsg = "error", callErr.Error()
	}
	rec := &usageRecord{
		Owner:        store.Owner,
		Organization: store.Owner,
		Model:        provider.Name,
		Provider:     provider.Name,
		Origin:       provider.Origin(),
		Currency:     "USD",
		Status:       status,
		ErrorMsg:     errMsg,
		AudioSeconds: seconds,
		ClientIP:     c.Ctx.Request.RemoteAddr,
		RequestID:    uuid.NewString(),
	}
	recordUsage(rec)
	recordTrace(c.Ctx.Request.Context(), rec, startTime)
}
