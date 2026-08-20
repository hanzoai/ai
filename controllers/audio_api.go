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

// MaxDecoded bounds what one request body may occupy in this process after it has
// been DECOMPRESSED, in bytes.
//
// It is a different question from what the socket admits (zip.Config.BodyLimit,
// set in app.go from the upload bound above): that caps what arrives, this caps
// what it becomes. A zstd frame's ratio on repetitive input runs to thousands to
// one, so a body small enough to accept can still ask for more heap than any pod
// has — and the decoder's own default ceiling is 64 GiB. Stating it is the whole
// protection.
//
// It lived in the in-house web router, which is gone. Both readers are here.
const MaxDecoded int64 = 1 << 26

// fits reports whether a transcription body is small enough to parse. Both
// transports ask this, so the bound is one expression with one name rather than
// the same comparison written twice, and it is answerable without standing up a
// request — which is what the two tests on it do.
//
// Stated as the acceptance rather than the refusal, because AudioSpeech below
// already has a local `oversize` and a package function of that name would sit
// shadowed inside it, readable as the bound while being a string.
func fits(body []byte) bool { return len(body) <= MaxTranscribeUpload }

// The transcription form has ONE reader: parseTranscribeForm, in zap_audio.go.
// It takes the body as a slice, which is what both transports hold — HTTP through
// the zip context, ZAP as the message it was handed — so neither needs a request
// object and neither carries its own copy of the parse.
//
// There were two. The HTTP leg read the same OpenAI form again through
// *http.Request, with the bound expressed as a MaxBytesReader and its overflow
// recovered by sniffing for *http.MaxBytesError in two places, because FormFile
// re-parses and the error can surface from either. Same five fields, same limit,
// twice — and its own comment said so.
//
// One thing genuinely goes with it: net/http's ParseMultipartForm merged the URL
// query into the form, so `?model=whisper` beside a multipart body used to
// resolve. Nothing crossing ZAP has a query string, so that spelling could never
// have worked on both transports; the OpenAI contract is a form field, and it is
// now the only way to send one.

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
	if err := json.Unmarshal(c.Body(), &req); err != nil {
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
	provider, authUser, upstreamModel, isPremium, err := c.authResolveProvider(token, req.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	// Zen family: /v1/audio/speech is the OpenAI-compat alias of zen's voice verb.
	if provider.Type == "Zen" {
		c.serveZenMedia("audio/voice", req.Model, c.Body(), 1, orgId, authUser, isPremium, time.Now().UTC())
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
	audioData, ttsResult, err := ttsProvider.QueryAudio(req.Input, c.Context(), c.GetAcceptLanguage())
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

	// The bound, then the parse — the same two steps in the same order as the ZAP
	// transport (zapAudioTranscribeHandler), against the same reader. The body
	// arrives as one slice, so the bound is a length and the parse is what it
	// guards: parsing is what turns one body into three more copies of it.
	body := c.Body()
	if !fits(body) {
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
	form, formErr := parseTranscribeForm(body)
	badReq := ""
	switch {
	case formErr != nil:
		// The reader's own sentence, which names WHICH part is wrong — the HTTP leg
		// used to flatten every parse failure into "requires a file part" and send a
		// caller looking for a part they had already sent.
		badReq = formErr.Error()
	case form.audio == nil:
		badReq = "audio request requires a \"file\" part"
	case form.model == "":
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
	model := form.model

	orgId := c.GetOrg()
	provider, authUser, upstreamModel, isPremium, err := c.authResolveProvider(token, model, orgId)
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
	heard, sttResult, err := sttProvider.ProcessAudio(form.audio, c.Context(), c.GetAcceptLanguage(), form.timings)
	if err != nil {
		c.recordAudioUsage(authUser, provider, model, isPremium, audioQuantity{}, "error", err.Error(), startTime)
		c.ResponseError(err.Error())
		return
	}
	c.recordAudioUsage(authUser, provider, model, isPremium, audioQuantity{seconds: sttSecondsOf(sttResult)}, "success", "", startTime)

	if form.responseFormat == "text" {
		c.SetHeader("Content-Type", "text/plain; charset=utf-8")
		c.Bytes(http.StatusOK, []byte(heard.Text))
		return
	}
	c.SetHeader("Content-Type", "application/json")
	c.Bytes(http.StatusOK, transcriptionBody(form.responseFormat, heard, sttSecondsOf(sttResult)))
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
		ClientIP:     c.Fiber().IP(),
		RequestID:    uuid.NewString(),
	}
	rec.bind(c.Context(), authUser)
	recordUsage(rec)
	recordTrace(c.Context(), rec, startTime)
}
