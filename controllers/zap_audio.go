// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// Native ZAP handlers for the audio route-group — the strangler migration of
// audio_api.go / zen_media.go / text_to_speech.go / speech_to_text.go off the controller layer.
//
// Pure ZAP handlers (no ApiController, no http.ResponseWriter): they
// re-implement each HTTP handler against object/ + iam, mirroring zapChatHandler
// exactly — identity from the auth seam (STEP 1), org = authUser.Owner (STEP 2),
// the SAME request structs (STEP 3), the SAME balance/KMS gates (STEP 4), the
// object-layer provider primitives (STEP 5), ONE meter (recordUsage +
// recordTrace, STEP 6), the SAME response bodies (STEP 7).
//
// Routes migrated (self-registered into the dispatch registry below — this file
// only CALLS registerCloud/registerGatewayPath from its own init()):
//
//   POST /v1/audio/speech                         -> audio.speech            (OpenAI TTS)
//   POST /v1/audio/transcriptions                 -> audio.transcribe        (OpenAI STT)
//   POST /v1/audio/voice                          -> audio.voice             (Zen)
//   POST /v1/audio/music                          -> audio.music             (Zen)
//   POST /v1/audio/foley                          -> audio.foley             (Zen)
//   POST /v1/generate-text-to-speech-audio        -> tts.generate            (legacy store-bound)
//   GET  /v1/generate-text-to-speech-audio-stream -> tts.generate.stream     (legacy, SSE buffered)
//   POST /v1/process-speech-to-text               -> stt.process             (legacy, multipart)
//
// The Zen media forward reuses the SHARED zapServeZenMedia/zapRecordZenMediaUsage
// (zap_images-generation.go) so there is ONE zen-media meter, not a fork.

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/tts"
)

// The canonical ZAP dispatch registry (registerCloud / registerGatewayPath /
// registerGatewayRoute + the lookups) lives in zap_registry.go. This group only
// self-registers from its own init().

func init() {
	registerZapAudio()
}

// registerZapAudio wires the audio group's handlers into the shared registry.
func registerZapAudio() {
	registerCloud("audio.speech", zapAudioSpeechHandler)
	registerCloud("audio.transcribe", zapAudioTranscribeHandler)
	registerCloud("audio.voice", zapAudioVerbHandler("voice"))
	registerCloud("audio.music", zapAudioVerbHandler("music"))
	registerCloud("audio.foley", zapAudioVerbHandler("foley"))
	registerCloud("tts.generate", zapLegacyTTSHandler)
	registerCloud("tts.generate.stream", zapLegacyTTSStreamHandler)
	registerCloud("stt.process", zapSTTHandler)

	registerGatewayPath("/v1/audio/speech", zapAudioSpeechHandler)
	registerGatewayPath("/v1/audio/transcriptions", zapAudioTranscribeHandler)
	registerGatewayPath("/v1/audio/voice", zapAudioVerbHandler("voice"))
	registerGatewayPath("/v1/audio/music", zapAudioVerbHandler("music"))
	registerGatewayPath("/v1/audio/foley", zapAudioVerbHandler("foley"))
	registerGatewayPath("/v1/generate-text-to-speech-audio-stream", zapLegacyTTSStreamHandler)
	registerGatewayPath("/v1/generate-text-to-speech-audio", zapLegacyTTSHandler)
	registerGatewayPath("/v1/process-speech-to-text", zapSTTHandler)
}

// ── audio.speech — OpenAI-compatible TTS (mirrors ApiController.AudioSpeech) ─

func zapAudioSpeechHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}
	var req audioSpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return object.BuildCloudResponse(400, nil, "invalid request: "+err.Error())
	}
	// The SAME bound the HTTP handler applies (MaxSpeechInput): one wire shape,
	// one limit, so neither transport is the cheap way in.
	//
	// Checked with the body's other shape checks, before identity is resolved,
	// for the reason the transcribe twin below refuses an unparseable body there:
	// a size is not a credential oracle — it says nothing about the credential —
	// and every byte of work after this point is work the caller has not yet
	// earned the right to buy.
	if len(req.Input) > MaxSpeechInput {
		return object.BuildCloudResponse(413, nil, fmt.Sprintf(
			"\"input\" exceeds the %d character limit", MaxSpeechInput))
	}

	// Identity + provider from the ONE auth seam (STEP 1). Auth is validated
	// before the body-field checks, so an invalid credential is 401 regardless of
	// body validity (never a probe-able 400), matching the HTTP handler.
	provider, authUser, upstreamModel, err := zapResolveAuth(auth, req.Model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	if req.Model == "" {
		return object.BuildCloudResponse(400, nil, "audio request requires a \"model\" field")
	}
	if req.Input == "" {
		return object.BuildCloudResponse(400, nil, "audio request requires an \"input\" field")
	}

	// Prepaid-balance gate — the ONE shared gate (STEP 4).
	if gateErr := enforceBalanceGate(authUser, "", req.Model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	isPremium := false
	if route := resolveModelRoute(req.Model); route != nil {
		isPremium = route.premium
	}

	// Zen family: /v1/audio/speech is the OpenAI-compat alias of zen's voice verb.
	if provider.Type == "Zen" {
		return zapServeZenMedia("audio/voice", req.Model, body, 1, authUser, isPremium, time.Now().UTC())
	}

	// KMS secret + upstream-model/voice binding (STEP 4/5), mirroring the HTTP path.
	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
	}
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if req.Model != "" {
		provider.SubType = req.Model
	}
	if req.Voice != "" {
		provider.Flavor = req.Voice
	}

	ttsProvider, err := provider.GetTextToSpeechProvider("en", req.ResponseFormat)
	if err != nil {
		return object.BuildCloudResponse(502, nil, err.Error())
	}

	startTime := time.Now().UTC()
	// Admission around the UPSTREAM CALL — the SAME ceiling the HTTP handler
	// takes, and the same per-org shares of it, so the two transports divide one
	// capacity rather than each getting a private copy. The key is the org
	// zapRecordAudioUsage bills below; this transport carries no header, so there
	// is nothing here that could name a different one.
	release, refused := admitSpeech(authUser.Owner)
	if refused != nil {
		return object.BuildCloudResponse(uint32(statusOf(refused)), nil, refused.Error())
	}
	defer release()

	audioData, ttsResult, err := ttsProvider.QueryAudio(req.Input, ctx, "en")
	spoken := audioQuantity{chars: ttsCharsOf(ttsResult, req.Input)}
	if err != nil {
		zapRecordAudioUsage(ctx, authUser, provider, req.Model, isPremium, audioQuantity{}, "error", err.Error(), startTime)
		return object.BuildCloudResponse(502, nil, err.Error())
	}
	if len(audioData) == 0 {
		zapRecordAudioUsage(ctx, authUser, provider, req.Model, isPremium, audioQuantity{}, "error", "empty audio data", startTime)
		return object.BuildCloudResponse(502, nil, "the audio data is nil")
	}
	zapRecordAudioUsage(ctx, authUser, provider, req.Model, isPremium, spoken, "success", "", startTime)

	// The native wire carries the audio bytes in the body; the requested format is
	// the provider's, so no transcode. (audioFormat's content-type hint is applied
	// by the gateway adapter when this is served over HTTP-over-ZAP.)
	return object.BuildCloudResponse(200, audioData, "")
}

// zapRecordAudioUsage mirrors ApiController.recordAudioUsage (STEP 6), quantity
// and all: the ZAP transport meters the same units through the same cost
// switches, so a call bills identically whichever door it arrived by. Errors
// trace either way.
func zapRecordAudioUsage(ctx context.Context, authUser *iam.User, provider *object.Provider, userModel string, isPremium bool, qty audioQuantity, status, errMsg string, startTime time.Time) {
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner:        authUser.Owner,
		User:         authUser.Owner + "/" + authUser.Name,
		Organization: authUser.Owner,
		Model:        userModel,
		Provider:     provider.Name,
		Currency:     "USD",
		Premium:      isPremium,
		Status:       status,
		ErrorMsg:     errMsg,
		AudioSeconds: qty.seconds,
		AudioChars:   qty.chars,
		RequestID:    uuid.NewString(),
	}
	rec.stampPayer(authUser)
	recordUsage(rec)
	recordTrace(ctx, rec, startTime)
}

// ── audio.transcribe — OpenAI-compatible STT (mirrors AudioTranscriptions) ──

func zapAudioTranscribeHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}
	// The SAME bound the HTTP handler applies (MaxTranscribeUpload). This
	// transport is handed the body as one slice, so the bound is checked on the
	// slice rather than installed on a reader — the bytes are already here, and
	// the check that matters is the one that keeps them from being PARSED into
	// three more copies and sent upstream.
	if len(body) > MaxTranscribeUpload {
		return object.BuildCloudResponse(413, nil, fmt.Sprintf(
			"audio upload exceeds the %d MiB limit", MaxTranscribeUpload>>20))
	}
	form, err := parseTranscribeForm(body)
	if err != nil {
		return object.BuildCloudResponse(400, nil, err.Error())
	}

	// Identity + provider from the ONE auth seam (STEP 1). Auth is validated
	// before the body-field checks, so an invalid credential is 401 regardless of
	// body validity (never a probe-able 400), matching the HTTP handler.
	provider, authUser, upstreamModel, err := zapResolveAuth(auth, form.model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	if form.audio == nil {
		return object.BuildCloudResponse(400, nil, "audio request requires a \"file\" part")
	}
	if form.model == "" {
		return object.BuildCloudResponse(400, nil, "audio request requires a \"model\" field")
	}

	// Prepaid-balance gate — the ONE shared gate (STEP 4).
	if gateErr := enforceBalanceGate(authUser, "", form.model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	isPremium := false
	if route := resolveModelRoute(form.model); route != nil {
		isPremium = route.premium
	}

	// No Zen STT verb exists; a Zen-routed model cannot serve this endpoint.
	if provider.Type == "Zen" {
		return object.BuildCloudResponse(400, nil, "model \""+form.model+"\" does not serve the /v1/audio/transcriptions endpoint")
	}

	// KMS secret + upstream-model/language binding (STEP 4/5), mirroring the HTTP path.
	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
	}
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else {
		provider.SubType = form.model
	}
	if form.language != "" {
		provider.Flavor = form.language
	}

	sttProvider, err := provider.GetSpeechToTextProvider("en")
	if err != nil {
		return object.BuildCloudResponse(502, nil, err.Error())
	}

	startTime := time.Now().UTC()
	// Admission around the UPSTREAM CALL — see the speech handler above.
	release, refused := admitSpeech(authUser.Owner)
	if refused != nil {
		return object.BuildCloudResponse(uint32(statusOf(refused)), nil, refused.Error())
	}
	defer release()

	text, sttResult, err := sttProvider.ProcessAudio(form.audio, ctx, "en")
	if err != nil {
		zapRecordAudioUsage(ctx, authUser, provider, form.model, isPremium, audioQuantity{}, "error", err.Error(), startTime)
		return object.BuildCloudResponse(502, nil, err.Error())
	}
	zapRecordAudioUsage(ctx, authUser, provider, form.model, isPremium, audioQuantity{seconds: sttSecondsOf(sttResult)}, "success", "", startTime)

	if form.responseFormat == "text" {
		return object.BuildCloudResponse(200, []byte(text), "")
	}
	data, _ := json.Marshal(transcriptionResponse{Text: text})
	return object.BuildCloudResponse(200, data, "")
}

// transcribeForm is the decoded OpenAI transcription multipart body.
type transcribeForm struct {
	model          string
	language       string
	responseFormat string
	audio          *bytes.Reader
}

// parseTranscribeForm reconstructs the OpenAI multipart form (file + model
// [+ language + response_format]) from the raw body, recovering the boundary
// from the body's own opening line exactly as parseSTTForm does — the handler
// stays on the shared (ctx, auth, body) signature. A missing "file" part is not
// an error here: auth is resolved first, so the handler reports it as a 400
// only to an authenticated caller.
func parseTranscribeForm(body []byte) (*transcribeForm, error) {
	boundary := multipartBoundary(body)
	if boundary == "" {
		return nil, fmt.Errorf("audio request requires a multipart form body")
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	form, err := mr.ReadForm(MaxTranscribeUpload)
	if err != nil {
		return nil, fmt.Errorf("read multipart form: %s", err.Error())
	}
	f := &transcribeForm{}
	first := func(key string) string {
		if v := form.Value[key]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	f.model = first("model")
	f.language = first("language")
	f.responseFormat = first("response_format")
	if files := form.File["file"]; len(files) > 0 {
		fh, err := files[0].Open()
		if err != nil {
			return nil, fmt.Errorf("open \"file\" part: %s", err.Error())
		}
		defer fh.Close()
		data, err := io.ReadAll(fh)
		if err != nil {
			return nil, fmt.Errorf("read \"file\" part: %s", err.Error())
		}
		f.audio = bytes.NewReader(data)
	}
	return f, nil
}

// ── audio.voice / .music / .foley — Zen-native (mirrors AudioMedia) ─────────

func zapAudioVerbHandler(verb string) zapHandler {
	return func(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
		if auth == "" {
			return object.BuildCloudResponse(401, nil, "authentication required")
		}
		switch verb {
		case "voice", "music", "foley":
		default:
			return object.BuildCloudResponse(404, nil, "unknown audio verb")
		}
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
			return object.BuildCloudResponse(400, nil, "audio request requires a \"model\" field")
		}

		provider, authUser, _, err := zapResolveAuth(auth, req.Model)
		if err != nil {
			return object.BuildCloudResponse(401, nil, err.Error())
		}
		if provider.Type != "Zen" {
			return object.BuildCloudResponse(400, nil, "model \""+req.Model+"\" does not serve the /v1/audio/"+verb+" endpoint")
		}
		isPremium := false
		if route := resolveModelRoute(req.Model); route != nil {
			isPremium = route.premium
		}
		return zapServeZenMedia("audio/"+verb, req.Model, body, 1, authUser, isPremium, time.Now().UTC())
	}
}

// ── Shared Zen media forward (buffered, no http.ResponseWriter) ─────────────
//
// The pure-ZAP mirror of serveZenMedia/pipeZenMedia (zen_media.go): reserve the
// per-unit cost, forward to zen, relay the upstream bytes, settle at the
// discovered price. Media is one buffered response — no writer to hold. This is
// the ONE zen-media meter every media group (audio, images, …) reuses; the image
// group's near-identical copy (zapVideoServeZen) is still to be folded in.

func zapServeZenMedia(apiPath, mdl string, rawBody []byte, units int, authUser *iam.User, isPremium bool, start time.Time) (*zap.Message, error) {
	var hold *budgetHold
	if authUser != nil {
		if zm, ok := zenFam.lookup(mdl); ok {
			subject := authUser.PayerSubject("")
			var ok2 bool
			if hold, ok2 = reserveBudget(subject, zm.unitCostCents(units)); !ok2 {
				return object.BuildCloudResponse(402, nil, object.InsufficientBalance(authUser.Owner, "cost").Message)
			}
		}
	}
	defer hold.settle(0)

	prov := object.ZenProvider()
	if prov == nil {
		return object.BuildCloudResponse(503, nil, "zen service is not configured")
	}
	reqID := uuid.NewString()

	hctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()

	hreq, err := http.NewRequestWithContext(hctx, http.MethodPost, prov.ProviderUrl+"/v1/"+apiPath, bytes.NewReader(rawBody))
	if err != nil {
		return object.BuildCloudResponse(500, nil, "build zen request: "+err.Error())
	}
	hreq.Header.Set("Content-Type", "application/json")
	if prov.ClientSecret != "" {
		hreq.Header.Set("Authorization", "Bearer "+prov.ClientSecret)
	}
	// Tenant attribution: zen needs a billable tenant, and ai — which settles the
	// ledger — tells zen it fronts this call so zen meters without double-charging.
	if authUser != nil {
		hreq.Header.Set("X-Org-Id", authUser.Owner)
	}
	hreq.Header.Set("X-Hanzo-Fronted-By", "ai")

	resp, err := zenPipeClient.Do(hreq)
	if err != nil {
		zapRecordZenMediaUsage(mdl, authUser, isPremium, reqID, units, start, hold, "error", err.Error())
		return object.BuildCloudResponse(502, nil, "zen request failed: "+err.Error())
	}
	defer resp.Body.Close()

	b, rErr := io.ReadAll(resp.Body)
	if rErr != nil {
		return object.BuildCloudResponse(502, nil, "read zen response: "+rErr.Error())
	}
	if resp.StatusCode != http.StatusOK {
		// A failed call is never charged: the deferred settle(0) releases the hold.
		return object.BuildCloudResponse(uint32(resp.StatusCode), nil, upstreamErrorMessage(b))
	}

	zapRecordZenMediaUsage(mdl, authUser, isPremium, reqID, units, start, hold, "success", "")
	return object.BuildCloudResponse(200, b, "")
}

// zapRecordZenMediaUsage settles the hold at the discovered per-unit price and
// records the served (or failed) call, mirroring recordZenMediaUsage (STEP 6).
func zapRecordZenMediaUsage(mdl string, authUser *iam.User, isPremium bool, reqID string, units int, start time.Time, hold *budgetHold, status, errMsg string) {
	var cents int64
	if status == "success" {
		if zm, ok := zenFam.lookup(mdl); ok {
			cents = zm.unitCostCents(units)
		}
	}
	hold.settle(cents)
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner: authUser.Owner, User: authUser.Owner + "/" + authUser.Name, Organization: authUser.Owner,
		Model: mdl, Provider: "zen",
		Cost: float64(cents) / 100.0, Currency: "USD",
		Premium: isPremium, Status: status, ErrorMsg: errMsg,
		RequestID: reqID, Account: "hanzo",
	}
	rec.stampPayer(authUser)
	recordUsage(rec)
	recordTrace(context.Background(), rec, start)
}

// ── Legacy store-bound TTS (mirrors GenerateTextToSpeechAudio) ──────────────

func zapLegacyTTSHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	// The legacy handler bills the STORE owner, but a caller identity is still
	// required — over ZAP there is no filter chain, so enforce auth here
	// (STEP 1). Empty/invalid auth -> 401.
	who, err := zapResolveUser(auth)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	var req TextToSpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return object.BuildCloudResponse(400, nil, "invalid request: "+err.Error())
	}
	message, chat, providerObj, pctx, err := object.PrepareTextToSpeech(req.StoreId, req.ProviderId, req.MessageId, req.Text, "en")
	if err != nil {
		return object.BuildCloudResponse(400, nil, err.Error())
	}

	// The same per-org ceiling the OpenAI-shaped door takes — this route reaches
	// the same models, so leaving it out would not be a smaller limit but no limit,
	// one path away. Keyed on the caller's own org rather than the store owner it
	// bills: the caller names the store, so the store cannot be what decides whose
	// capacity is spent.
	release, refused := admitSpeech(orgOf(who))
	if refused != nil {
		return object.BuildCloudResponse(uint32(statusOf(refused)), nil, refused.Error())
	}
	defer release()

	startTime := time.Now().UTC()
	audioData, ttsResult, err := providerObj.QueryAudio(message.Text, pctx, "en")
	if err != nil {
		return object.BuildCloudResponse(502, nil, err.Error())
	}
	if audioData == nil {
		return object.BuildCloudResponse(502, nil, "the audio data is nil")
	}
	if err := object.UpdateChatStats(chat, ttsResult); err != nil {
		return object.BuildCloudResponse(500, nil, err.Error())
	}
	zapRecordLegacyTTSUsage(ctx, chat, req.ProviderId, ttsResult, startTime)
	return object.BuildCloudResponse(200, audioData, "")
}

// zapLegacyTTSStreamHandler buffers the SSE stream (no ResponseWriter over ZAP)
// and returns it as one body, mirroring GenerateTextToSpeechAudioStream. The
// store/message ids arrive as body JSON (native cloud) or, over the gateway,
// packed from the request query into the same shape.
func zapLegacyTTSStreamHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, err := zapResolveUser(auth); err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	storeId, messageId := ttsStreamParams(body)
	message, chat, providerObj, pctx, err := object.PrepareTextToSpeech(storeId, "", messageId, "", "en")
	if err != nil {
		return object.BuildCloudResponse(400, nil, err.Error())
	}
	startTime := time.Now().UTC()
	var buf bytes.Buffer
	ttsResult, err := providerObj.QueryAudioStream(message.Text, pctx, &buf, "en")
	if err != nil {
		return object.BuildCloudResponse(502, nil, err.Error())
	}
	// UpdateChatStats failure is non-fatal in the HTTP path (logged), keep parity.
	if err := object.UpdateChatStats(chat, ttsResult); err != nil {
		log.Error("ZAP: tts stream UpdateChatStats: %v", err)
	}
	zapRecordLegacyTTSUsage(ctx, chat, "", ttsResult, startTime)
	return object.BuildCloudResponse(200, buf.Bytes(), "")
}

// ttsStreamParams reads storeId/messageId from either a JSON body ({"storeId":…,
// "messageId":…}) or a raw URL query string (storeId=…&messageId=…) — the GET
// endpoint's params, however the caller packed them.
func ttsStreamParams(body []byte) (storeId, messageId string) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var p struct {
			StoreId   string `json:"storeId"`
			MessageId string `json:"messageId"`
		}
		if json.Unmarshal(trimmed, &p) == nil {
			return p.StoreId, p.MessageId
		}
	}
	if q, err := url.ParseQuery(string(trimmed)); err == nil {
		return q.Get("storeId"), q.Get("messageId")
	}
	return "", ""
}

// zapRecordLegacyTTSUsage mirrors recordLegacyTTSUsage (STEP 6): trace always,
// debit only a USD-priced result (a non-USD float must not debit as dollars).
func zapRecordLegacyTTSUsage(ctx context.Context, chat *object.Chat, providerId string, ttsResult *tts.TextToSpeechResult, startTime time.Time) {
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
		RequestID:    uuid.NewString(),
	}
	if rec.Currency == "" || rec.Currency == "USD" {
		rec.Currency = "USD"
		recordUsage(rec)
	}
	recordTrace(ctx, rec, startTime)
}

// ── Legacy store-bound STT (mirrors ProcessSpeechToText) ────────────────────

func zapSTTHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	who, err := zapResolveUser(auth)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	storeId, audioReader, err := parseSTTForm(body)
	if err != nil {
		return object.BuildCloudResponse(400, nil, err.Error())
	}
	if storeId == "" {
		return object.BuildCloudResponse(400, nil, "Missing required parameter: storeId")
	}
	store, err := object.GetStore(storeId)
	if err != nil {
		return object.BuildCloudResponse(400, nil, err.Error())
	}
	if store == nil {
		return object.BuildCloudResponse(400, nil, fmt.Sprintf("The store: %s is not found", storeId))
	}
	provider, err := store.GetSpeechToTextProvider()
	if err != nil {
		return object.BuildCloudResponse(400, nil, err.Error())
	}
	if provider == nil {
		return object.BuildCloudResponse(400, nil, fmt.Sprintf("The speech-to-text provider for store: %s is not found", store.GetId()))
	}
	providerObj, err := provider.GetSpeechToTextProvider("en")
	if err != nil {
		return object.BuildCloudResponse(502, nil, err.Error())
	}

	// The same per-org ceiling every other speech door takes — see the legacy TTS
	// twin above for why the key is the caller's org and not the store's.
	release, refused := admitSpeech(orgOf(who))
	if refused != nil {
		return object.BuildCloudResponse(uint32(statusOf(refused)), nil, refused.Error())
	}
	defer release()

	startTime := time.Now().UTC()
	text, legacyResult, err := providerObj.ProcessAudio(audioReader, ctx, "en")
	zapRecordLegacySTTUsage(ctx, store, provider, sttSecondsOf(legacyResult), startTime, err)
	if err != nil {
		return object.BuildCloudResponse(502, nil, err.Error())
	}
	data, _ := json.Marshal(Response{Status: "ok", Data: text})
	return object.BuildCloudResponse(200, data, "")
}

// parseSTTForm reconstructs the multipart form (audio file + storeId) from the
// raw body. The multipart body is self-describing — its opening line is
// `--<boundary>` — so the boundary is recovered without a Content-Type header,
// keeping the handler on the shared (ctx, auth, body) signature.
func parseSTTForm(body []byte) (string, *bytes.Reader, error) {
	boundary := multipartBoundary(body)
	if boundary == "" {
		return "", nil, fmt.Errorf("Error getting audio audioFile: body is not a multipart form")
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	form, err := mr.ReadForm(MaxTranscribeUpload)
	if err != nil {
		return "", nil, fmt.Errorf("read multipart form: %s", err.Error())
	}
	storeId := ""
	if v := form.Value["storeId"]; len(v) > 0 {
		storeId = v[0]
	}
	files := form.File["audio"]
	if len(files) == 0 {
		return storeId, nil, fmt.Errorf("Error getting audio audioFile: no \"audio\" file part")
	}
	f, err := files[0].Open()
	if err != nil {
		return storeId, nil, fmt.Errorf("Error getting audio audioFile: %s", err.Error())
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return storeId, nil, fmt.Errorf("Error getting audio audioFile: %s", err.Error())
	}
	return storeId, bytes.NewReader(data), nil
}

// multipartBoundary derives the boundary from the opening `--<boundary>` line of
// a multipart body.
func multipartBoundary(body []byte) string {
	nl := bytes.IndexByte(body, '\n')
	if nl < 0 {
		return ""
	}
	first := strings.TrimRight(string(body[:nl]), "\r")
	if !strings.HasPrefix(first, "--") {
		return ""
	}
	return strings.TrimPrefix(first, "--")
}

// zapRecordLegacySTTUsage mirrors recordLegacySTTUsage (STEP 6): the store-bound
// path meters the same audio seconds as the OpenAI-shaped one, so legacy traffic
// is measured rather than merely counted. Trace always emitted (errors stay
// visible).
func zapRecordLegacySTTUsage(ctx context.Context, store *object.Store, provider *object.Provider, seconds float64, startTime time.Time, callErr error) {
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
		Currency:     "USD",
		Status:       status,
		ErrorMsg:     errMsg,
		AudioSeconds: seconds,
		RequestID:    uuid.NewString(),
	}
	recordUsage(rec)
	recordTrace(ctx, rec, startTime)
}
