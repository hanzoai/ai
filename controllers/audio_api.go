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
	"mime/multipart"
	"net/http"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// audioFormMaxMemory is how much of a multipart audio body is held in memory
// before Go spills the rest to a temp file. It matches the bound the ZAP reader
// gives the same wire shape (parseTranscribeForm), so the two transports accept
// the same requests.
const audioFormMaxMemory = 64 << 20

// transcribeRequest is the OpenAI /v1/audio/transcriptions multipart request as
// read from an HTTP request: the audio part, plus the fields beside it.
type transcribeRequest struct {
	file           multipart.File
	model          string
	language       string
	responseFormat string
}

// readTranscribeRequest parses the multipart body ONCE and reads every field
// from it in one place.
//
// The parse has to come first, and putting all the reads behind it is the point.
// beego resolves GetString through r.Form, and for multipart/form-data Go fills
// r.Form only inside ParseMultipartForm. Nothing had called it, so a field read
// came back empty unless something else had already parsed the body — and
// reading the FILE was that something. `model` was read one line above the file
// read, so it was always empty: this endpoint answered `requires a "model"
// field` to every request that carried one, and every OpenAI client sends model
// as a form field. Only a caller who happened to pass it in the query string got
// through.
//
// ParseMultipartForm also merges the URL query into r.Form, so a field passed
// either way still resolves and the query-string spelling keeps working.
//
// A body that will not parse leaves the fields empty and returns the file error,
// which the caller reports as the 400 it is.
//
// The ZAP transport reads the same wire shape from a raw body in
// parseTranscribeForm; both readers are tested against the OpenAI form.
func readTranscribeRequest(r *http.Request) (transcribeRequest, error) {
	_ = r.ParseMultipartForm(audioFormMaxMemory)

	req := transcribeRequest{
		model:          r.Form.Get("model"),
		language:       r.Form.Get("language"),
		responseFormat: r.Form.Get("response_format"),
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return req, err
	}
	req.file = file
	return req, nil
}

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

// AudioTranscriptions is the OpenAI-compatible STT endpoint
// (POST /v1/audio/transcriptions, multipart: file + model [+ language +
// response_format]). It mirrors AudioSpeech exactly: authenticate the caller,
// resolve `model` to its STT provider through the SAME model-route resolution
// (so the in-cluster speech service — or any BYO node registered as an STT
// provider — works transparently), transcribe, and return the OpenAI body.
// One code path, OpenAI-shaped, no store coupling (unlike the legacy
// /v1/process-speech-to-text, which is bound to a chat store).
//
// @Title AudioTranscriptions
// @Tag Audio API
// @Description OpenAI-compatible speech-to-text
// @Param file formData file true "the audio to transcribe"
// @Param model formData string true "STT model (whisper family)"
// @Success 200 {object} controllers.transcriptionResponse "transcription"
// @router /audio/transcriptions [post]
func (c *ApiController) AudioTranscriptions() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}

	form, fileErr := readTranscribeRequest(c.Ctx.Request)
	model := form.model
	badReq := ""
	if fileErr != nil {
		badReq = "audio request requires a \"file\" part"
	} else if model == "" {
		badReq = "audio request requires a \"model\" field"
	}
	if badReq != "" {
		// Authenticate before reporting the client error: an invalid credential is 401
		// regardless of body validity (never a probe-able 200/400), matching speech.
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, badReq)
		return
	}
	defer form.file.Close()

	orgId := c.GetOrg()
	provider, authUser, upstreamModel, isPremium, _, err := c.authResolveProvider(token, model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	// No Zen STT verb exists; a Zen-routed model cannot serve this endpoint.
	if provider.Type == "Zen" {
		c.ResponseErrorWithStatus(http.StatusBadRequest, "model \""+model+"\" does not serve the /v1/audio/transcriptions endpoint")
		return
	}
	// Bind the resolved upstream model + the audio language hint onto the provider
	// before constructing the STT client (model in SubType, language in Flavor —
	// the STT mirror of speech's voice-in-Flavor binding).
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else {
		provider.SubType = model
	}
	if form.language != "" {
		provider.Flavor = form.language
	}

	sttProvider, err := provider.GetSpeechToTextProvider(c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	startTime := time.Now().UTC()
	text, _, err := sttProvider.ProcessAudio(form.file, c.Ctx.Request.Context(), c.GetAcceptLanguage())
	if err != nil {
		c.recordAudioUsage(authUser, provider, model, isPremium, "error", err.Error(), startTime)
		c.ResponseError(err.Error())
		return
	}
	c.recordAudioUsage(authUser, provider, model, isPremium, "success", "", startTime)

	if form.responseFormat == "text" {
		c.Ctx.Output.Header("Content-Type", "text/plain; charset=utf-8")
		c.Ctx.Output.Body([]byte(text))
		return
	}
	body, _ := json.Marshal(transcriptionResponse{Text: text})
	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Body(body)
}

// transcriptionResponse is the OpenAI /v1/audio/transcriptions `json` body.
type transcriptionResponse struct {
	Text string `json:"text"`
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
	rec.stampPayer(authUser)
	recordUsage(rec)
	recordTrace(c.Ctx.Request.Context(), rec, startTime)
}
