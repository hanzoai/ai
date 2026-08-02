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

package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// audioSpeechRequest is the OpenAI /v1/audio/speech body: synthesize `input` with
// `model`'s voice. Mirrors the OpenAI TTS API so the same client SDKs work unchanged.
type audioSpeechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice,omitempty"`           // maps to the provider's flavor/voice when set
	ResponseFormat string  `json:"response_format,omitempty"` // mp3 (default) | wav | opus | …
	Speed          float64 `json:"speed,omitempty"`           // accepted, provider-dependent
}

// AudioSpeech is the OpenAI-compatible TTS endpoint (POST /v1/audio/speech). It
// authenticates the caller, resolves `model` to its TTS provider (the SAME model-route
// resolution the chat/images/video endpoints use — so a BYO node registered as a TTS
// provider works transparently), synthesizes the audio, and streams the bytes back.
// One code path, OpenAI-shaped, no store/message coupling (unlike the legacy
// /v1/generate-text-to-speech-audio which is bound to a chat store).
//
// @Title AudioSpeech
// @Tag Audio API
// @Description OpenAI-compatible text-to-speech
// @Param body body controllers.audioSpeechRequest true "speech request"
// @Success 200 {file} audio "audio bytes"
// @router /audio/speech [post]
func (c *ApiController) AudioSpeech() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}

	var req audioSpeechRequest
	badReq := ""
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", err.Error())
	} else if req.Model == "" {
		badReq = "audio request requires a \"model\" field"
	} else if req.Input == "" {
		badReq = "audio request requires an \"input\" field"
	}
	if badReq != "" {
		// Authenticate before reporting the client error: an invalid credential is 401
		// regardless of body validity (never a probe-able 200/400), matching images.
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, badReq)
		return
	}

	orgId := c.GetOrg()
	provider, authUser, upstreamModel, isPremium, _, err := c.authResolveProvider(token, req.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	// Zen family: /v1/audio/speech is the OpenAI-compat alias of zen's voice verb.
	if provider.Type == "Zen" {
		c.serveZenMedia("audio/voice", req.Model, c.Ctx.Input.RequestBody, 1, orgId, authUser, isPremium, time.Now().UTC())
		return
	}
	// Bind the resolved upstream model + the requested voice onto the provider before
	// constructing the TTS client (the provider carries model in SubType, voice in
	// Flavor — a BYO/OpenAI-compat provider reads both).
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if req.Model != "" {
		provider.SubType = req.Model
	}
	if req.Voice != "" {
		provider.Flavor = req.Voice
	}

	ttsProvider, err := provider.GetTextToSpeechProvider(c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	startTime := time.Now().UTC()
	audioData, _, err := ttsProvider.QueryAudio(req.Input, c.Ctx.Request.Context(), c.GetAcceptLanguage())
	if err != nil {
		c.recordAudioUsage(authUser, provider, req.Model, isPremium, "error", err.Error(), startTime)
		c.ResponseError(err.Error())
		return
	}
	if len(audioData) == 0 {
		c.recordAudioUsage(authUser, provider, req.Model, isPremium, "error", "empty audio data", startTime)
		c.ResponseError(c.T("tts:The audio data is nil"))
		return
	}
	c.recordAudioUsage(authUser, provider, req.Model, isPremium, "success", "", startTime)

	contentType, filename := audioFormat(req.ResponseFormat)
	c.ResponseAudio(audioData, contentType, filename)
}

// audioFormat maps the OpenAI response_format to a content type + filename. Defaults
// to mp3 (OpenAI's default); the synthesis format is the provider's, so this is the
// wire content-type hint, not a transcode.
func audioFormat(format string) (contentType, filename string) {
	switch format {
	case "wav":
		return "audio/wav", "speech.wav"
	case "opus":
		return "audio/opus", "speech.opus"
	case "aac":
		return "audio/aac", "speech.aac"
	case "flac":
		return "audio/flac", "speech.flac"
	default:
		return "audio/mpeg", "speech.mp3"
	}
}

// recordAudioUsage records a direct-provider TTS call for billing +
// observability, mirroring recordImageUsage. There is no direct-provider audio
// price table yet (the Zen "audio/voice" SKU prices itself in serveZenMedia),
// so the row bills 0 and is flagged Unpriced — the traffic is VISIBLE in the
// warehouse/o11y and flagged for pricing instead of invisible. recordUsage
// filters error rows; the trace is emitted either way.
func (c *ApiController) recordAudioUsage(authUser *iam.User, provider *object.Provider, userModel string, isPremium bool, status, errMsg string, startTime time.Time) {
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner:        c.billingOrg(authUser),
		User:         authUser.Owner + "/" + authUser.Name,
		Organization: authUser.Owner,
		Model:        userModel,
		Provider:     provider.Name,
		Currency:     "USD",
		Premium:      isPremium,
		Status:       status,
		ErrorMsg:     errMsg,
		Unpriced:     true,
		ClientIP:     c.Ctx.Request.RemoteAddr,
		RequestID:    uuid.NewString(),
	}
	recordUsage(rec)
	recordTrace(c.Ctx.Request.Context(), rec, startTime)
}
