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
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/stt"
	"github.com/hanzoai/ai/tts"
)

// MaxTranscribeUpload bounds the audio one transcription request may carry. It
// is OpenAI's own /v1/audio/transcriptions limit, so every OpenAI-compatible
// client already chunks below it and none has to learn a Hanzo-specific number.
//
// It is also the multipart parser's memory bound, deliberately the SAME number:
// a form that cannot exceed this can never spill a temp file, so the disk never
// participates in an upload at all and there is one limit to reason about
// instead of a wire limit and a spill limit that can drift apart.
//
// Size is not the whole bound. Bytes do not determine how much AUDIO they carry
// — the same 25 MiB is ~13 min of 16 kHz PCM or hours of low-bitrate Opus — so
// the work a request can buy is bounded by the per-org share of the speech
// ceiling, not by this. See admitSpeech.
const MaxTranscribeUpload = 25 << 20

// MaxSpeechInput bounds the text one synthesis request may carry, in bytes of
// UTF-8. It is OpenAI's /v1/audio/speech limit, for the same reason.
//
// Synthesis cost is LINEAR in input length and the length is known before any
// work starts, so unlike transcription this bound is exact: it caps the work,
// not merely the bytes.
const MaxSpeechInput = 4096

// transcribeRequest is the OpenAI /v1/audio/transcriptions multipart request as
// read from an HTTP request: the audio part, plus the fields beside it.
type transcribeRequest struct {
	file           multipart.File
	model          string
	language       string
	responseFormat string
	timings        []stt.Timing
}

// readTranscribeRequest parses the multipart body ONCE and reads every field
// from it in one place.
//
// The parse has to come first, and putting all the reads behind it is the point.
// the router resolves GetString through r.Form, and for multipart/form-data Go fills
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
// The upload bound is installed on the BODY, before the parse, so the bytes are
// refused as they arrive rather than measured after they have been accepted: a
// length checked afterwards has already cost the memory it was meant to
// prevent, and a multipart body has no trustworthy length to check anyway
// (Content-Length is the client's claim, and a chunked body states none).
// oversize reports that the reader hit the bound, which the caller answers 413.
//
// The ZAP transport reads the same wire shape from a raw body in
// parseTranscribeForm; both readers are tested against the OpenAI form and both
// enforce MaxTranscribeUpload.
func readTranscribeRequest(r *http.Request) (req transcribeRequest, oversize bool, err error) {
	r.Body = http.MaxBytesReader(nil, r.Body, MaxTranscribeUpload)
	parseErr := r.ParseMultipartForm(MaxTranscribeUpload)

	req = transcribeRequest{
		model:          r.Form.Get("model"),
		language:       r.Form.Get("language"),
		responseFormat: r.Form.Get("response_format"),
	}
	if req.timings, err = timingsOf(r.Form); err != nil {
		return req, false, err
	}
	var tooLarge *http.MaxBytesError
	if errors.As(parseErr, &tooLarge) {
		return req, true, parseErr
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		// FormFile re-parses on a body the bound already cut, so the overflow can
		// surface here instead of above.
		if errors.As(err, &tooLarge) {
			return req, true, err
		}
		return req, false, err
	}
	req.file = file
	return req, false, nil
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
// This is the ONE way to synthesize speech: OpenAI-shaped, with no store or
// message coupling, so a caller needs no chat to speak.
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
	oversize := ""
	badReq := ""
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", err.Error())
	} else if req.Model == "" {
		badReq = "audio request requires a \"model\" field"
	} else if req.Input == "" {
		badReq = "audio request requires an \"input\" field"
	} else if len(req.Input) > MaxSpeechInput {
		oversize = fmt.Sprintf("\"input\" exceeds the %d character limit", MaxSpeechInput)
	}
	if oversize != "" {
		// Authenticate before reporting the size, for the reason the 400 below gives.
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusRequestEntityTooLarge, oversize)
		return
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

	ttsProvider, err := provider.GetTextToSpeechProvider(c.GetAcceptLanguage(), req.ResponseFormat)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Admission is taken around the UPSTREAM CALL only — the work — so a slot is
	// never held across authentication or provider resolution. It is keyed on the
	// org that PAYS for this call, which is the same expression recordAudioUsage
	// bills below: the tenant whose share is spent is the tenant who is charged.
	release, refused := admitSpeech(c.billingOrg(authUser))
	if refused != nil {
		c.ResponseErrorWithStatus(statusOf(refused), refused.Error())
		return
	}
	defer release()

	startTime := time.Now().UTC()
	audioData, ttsResult, err := ttsProvider.QueryAudio(req.Input, c.Ctx.Request.Context(), c.GetAcceptLanguage())
	spoken := audioQuantity{chars: ttsCharsOf(ttsResult, req.Input)}
	if err != nil {
		c.recordAudioUsage(authUser, provider, req.Model, isPremium, audioQuantity{}, "error", err.Error(), startTime)
		c.ResponseError(err.Error())
		return
	}
	if len(audioData) == 0 {
		c.recordAudioUsage(authUser, provider, req.Model, isPremium, audioQuantity{}, "error", "empty audio data", startTime)
		c.ResponseError(c.T("tts:The audio data is nil"))
		return
	}
	c.recordAudioUsage(authUser, provider, req.Model, isPremium, spoken, "success", "", startTime)

	contentType, filename := audioResponseLabel(ttsResult.ContentType, req.ResponseFormat)
	c.ResponseAudio(audioData, contentType, filename)
}

// audioResponseLabel names the bytes that were actually synthesized.
//
// The provider's REPORTED media type wins, and the requested format is only the
// fallback for a provider that did not say. Labelling from the request is how
// /v1/audio/speech came to answer `Content-Type: audio/opus` carrying an MP3:
// the upstream silently substitutes its default for a container it cannot make,
// and the gateway repeated the request back as though it were a fact. A player
// handed the wrong container is entitled to refuse it, so this is a correctness
// bug and not a cosmetic one.
// It is the ONLY way to label an audio response, deliberately: the request-only
// mapping used to be a callable function of its own, so the wrong answer stayed
// one edit away and a test could not stop someone reaching for it again. Folding
// it in as the unreachable-by-itself fallback makes labelling from the request
// alone not something the code can express.
func audioResponseLabel(actual, requested string) (contentType, filename string) {
	if actual != "" {
		return actual, "speech" + audioExtension(actual)
	}
	// No media type from the provider: the request is the only information
	// there is, and it was the entire contract before this.
	switch requested {
	case "wav":
		return "audio/wav", "speech.wav"
	case "opus":
		return "audio/opus", "speech.opus"
	case "aac":
		return "audio/aac", "speech.aac"
	case "flac":
		return "audio/flac", "speech.flac"
	default:
		return "audio/mpeg", "speech.mp3" // OpenAI's default
	}
}

// audioExtension maps a media type to the file extension a browser download
// should carry. Unknown types get no extension rather than a guessed one.
func audioExtension(contentType string) string {
	switch contentType {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/opus", "audio/ogg":
		return ".opus"
	case "audio/aac":
		return ".aac"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/pcm", "application/octet-stream":
		return ".pcm"
	default:
		return ""
	}
}

// AudioTranscriptions is the OpenAI-compatible STT endpoint
// (POST /v1/audio/transcriptions, multipart: file + model [+ language +
// response_format]). It mirrors AudioSpeech exactly: authenticate the caller,
// resolve `model` to its STT provider through the SAME model-route resolution
// (so the in-cluster speech service — or any BYO node registered as an STT
// provider — works transparently), transcribe, and return the OpenAI body.
// This is the ONE way to transcribe: OpenAI-shaped, with no store coupling, so a
// caller needs no chat to be heard.
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

	form, oversize, fileErr := readTranscribeRequest(c.Ctx.Request)
	model := form.model
	if oversize {
		// Authenticate first, for the same reason the 400s below do: the size of a
		// body is not something an unauthenticated caller gets to learn.
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusRequestEntityTooLarge, fmt.Sprintf(
			"audio upload exceeds the %d MiB limit", MaxTranscribeUpload>>20))
		return
	}
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

	// Admission is taken around the UPSTREAM CALL only — see AudioSpeech.
	release, refused := admitSpeech(c.billingOrg(authUser))
	if refused != nil {
		c.ResponseErrorWithStatus(statusOf(refused), refused.Error())
		return
	}
	defer release()

	startTime := time.Now().UTC()
	heard, sttResult, err := sttProvider.ProcessAudio(form.file, c.Ctx.Request.Context(), c.GetAcceptLanguage(), form.timings)
	if err != nil {
		c.recordAudioUsage(authUser, provider, model, isPremium, audioQuantity{}, "error", err.Error(), startTime)
		c.ResponseError(err.Error())
		return
	}
	c.recordAudioUsage(authUser, provider, model, isPremium, audioQuantity{seconds: sttSecondsOf(sttResult)}, "success", "", startTime)

	if form.responseFormat == "text" {
		c.Ctx.Output.Header("Content-Type", "text/plain; charset=utf-8")
		c.Ctx.Output.Body([]byte(heard.Text))
		return
	}
	c.Ctx.Output.Header("Content-Type", "application/json")
	c.Ctx.Output.Body(transcriptionBody(form.responseFormat, heard, sttSecondsOf(sttResult)))
}

// transcriptionResponse is the OpenAI /v1/audio/transcriptions body. `json`
// carries the text alone; `verbose_json` adds what the decode measured — the
// duration that meters the call, and the timings the caller asked for. The
// omitempty tags are what keep the plain body exactly {"text"}.
type transcriptionResponse struct {
	Task     string             `json:"task,omitempty"`
	Duration float64            `json:"duration,omitempty"`
	Text     string             `json:"text"`
	Words    []stt.TimedWord    `json:"words,omitempty"`
	Segments []stt.TimedSegment `json:"segments,omitempty"`
}

// transcriptionBody renders a transcript in the format the caller asked for.
// Both transports answer with this, so there is one definition of the shape.
func transcriptionBody(format string, t *stt.Transcript, seconds float64) []byte {
	res := transcriptionResponse{Text: t.Text}
	if format == "verbose_json" {
		res.Task = "transcribe"
		res.Duration = seconds
		res.Words = t.Words
		res.Segments = t.Segments
	}
	body, _ := json.Marshal(res)
	return body
}

// timingsOf reads OpenAI's timestamp_granularities out of a parsed form, in
// either spelling — the OpenAI SDKs bracket a multipart array name and
// hand-rolled clients send it bare, and reading only one leaves half the
// callers silently untimed. Both transports parse the same wire shape, so both
// read it here.
func timingsOf(form map[string][]string) ([]stt.Timing, error) {
	var want []stt.Timing
	seen := map[stt.Timing]bool{}
	for _, key := range [...]string{"timestamp_granularities[]", "timestamp_granularities"} {
		for _, v := range form[key] {
			t := stt.Timing(v)
			if t != stt.Word && t != stt.Segment {
				return nil, fmt.Errorf("unknown timestamp_granularities %q; supported: %s, %s", v, stt.Segment, stt.Word)
			}
			if !seen[t] {
				seen[t] = true
				want = append(want, t)
			}
		}
	}
	return want, nil
}

// audioQuantity is what an audio call actually consumed: seconds of audio for a
// transcription, characters for a synthesis. A call is one direction or the
// other, never both, and the zero value is what an ERROR consumed — nothing
// was delivered, so nothing is billed, while the row and its trace still appear.
//
// It travels as one value rather than two positional arguments so that adding
// the quantity to an emit site cannot silently pass a duration where a character
// count belongs; the two are both numbers and mean entirely different money.
type audioQuantity struct {
	seconds float64
	chars   int
}

// sttSecondsOf reads the transcribed duration from a provider result, or 0 when
// the upstream did not report one. 0 meters nothing rather than guessing: bytes
// do not imply duration (the same megabyte is minutes of PCM or hours of Opus),
// so an unreported duration is a gap to see in the data, not a number to invent.
func sttSecondsOf(result *stt.SpeechToTextResult) float64 {
	if result == nil {
		return 0
	}
	return result.AudioDurationSeconds
}

// ttsCharsOf reads the synthesized character count from a provider result,
// falling back to the length of the text we ASKED it to speak. The fallback is
// sound where the STT one would not be: synthesis cost is linear in the input,
// and the input is known before the call — so this is the quantity, not an
// estimate of it.
func ttsCharsOf(result *tts.TextToSpeechResult, input string) int {
	if result != nil && result.TokenCount > 0 {
		return result.TokenCount
	}
	return len([]rune(input))
}

// recordAudioUsage records a direct-provider audio call for billing +
// observability, mirroring recordImageUsage.
//
// The quantity is the whole point. Every emit site used to discard the provider
// result (`text, _, err :=`) and hardcode `Unpriced: true`, so the record reached
// the cost switches carrying nothing to multiply and audio billed 0 no matter
// what it consumed. The record now carries what was used, and the Unpriced flag
// is DERIVED from whether a rate is configured (recordUnpriced → audioPriced)
// rather than asserted here — so the day a rate is set, this function does not
// change. recordUsage filters error rows; the trace is emitted either way.
func (c *ApiController) recordAudioUsage(authUser *iam.User, provider *object.Provider, userModel string, isPremium bool, qty audioQuantity, status, errMsg string, startTime time.Time) {
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner:        c.billingOrg(authUser),
		Organization: authUser.Owner,
		Model:        userModel,
		Provider:     provider.Name,
		Origin:       provider.Origin(),
		Currency:     "USD",
		Premium:      isPremium,
		Status:       status,
		ErrorMsg:     errMsg,
		AudioSeconds: qty.seconds,
		AudioChars:   qty.chars,
		ClientIP:     c.Ctx.Request.RemoteAddr,
		RequestID:    uuid.NewString(),
	}
	rec.bind(c.Ctx.Request.Context(), authUser)
	recordUsage(rec)
	recordTrace(c.Ctx.Request.Context(), rec, startTime)
}
