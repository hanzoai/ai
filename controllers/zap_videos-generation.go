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

// Native ZAP handlers for the async video route-group — the pure-ZAP twin of the
// beego ApiController.VideosGenerations / .RetrieveVideo / .VideoContent methods
// (videos_api.go). They re-implement the SAME auth → provider-resolution →
// balance-gate → reservation → upstream → meter pipeline against object/ +
// model/ directly, with NO http.ResponseWriter and NO beego controller. The
// async lifecycle (create → poll → download), the in-pod videoJobStore, the
// per-video reservation and the EXACTLY-ONCE completion debit are the shared
// authority in video_jobs.go / videos_api.go — this file reuses it verbatim so
// there is one job store and one billing ledger across both transports.
//
// Beego keeps serving POST /v1/videos/generations, GET /v1/videos/:id and
// GET /v1/videos/:id/content via router.go in parallel (strangler) until
// Integrate flips the native dispatch and deletes the beego lines.
//
// Registration convention (recipe): this group self-registers from its OWN
// init(); it never redefines the registry primitives (registerCloud /
// registerGatewayPath + their maps + lookups) — those are the ONE-TIME shared
// infra whose single home is zap_embeddings-rerank.go.
//
// Native-cloud (MsgType 100) dispatches by METHOD name, which is distinct per
// route, so all three routes register cleanly with the id carried in the body
// for poll/download. The HTTP-over-ZAP (MsgType 200) path dispatches by prefix
// through a handler that only receives (ctx, auth, body): that is sufficient for
// the POST create (registered here), but the two GET routes carry their id in
// the URL PATH — which the current shared registry handler signature does not
// forward. Wiring the gateway GET routes therefore rides on Integrate extending
// the gateway dispatch to pass the request path/method to the handler; the
// complete logic is already here (zapVideoRetrieve / zapVideoContent, reached
// today via the native-cloud methods) and needs no change when that lands.

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/account"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// ── Group self-registration ───────────────────────────────────────────────

func init() {
	// Native cloud (MsgType 100): one method per lifecycle verb; the poll/download
	// verbs take the job id in the body ({"id":"video_…"}).
	registerCloud("videos.generate", zapVideosGenerateHandler)
	registerCloud("videos.retrieve", zapVideosRetrieveHandler)
	registerCloud("videos.content", zapVideosContentHandler)

	// Gateway (MsgType 200): the POST create is fully served by (ctx, auth, body).
	// A more specific prefix than any GET route so longest-prefix match always
	// picks create for /v1/videos/generations.
	registerGatewayPath("/v1/videos/generations", zapVideosGenerateHandler)
}

// ── videos.generate / POST /v1/videos/generations ─────────────────────────

// zapVideosGenerateHandler is the native twin of ApiController.VideosGenerations.
// It authenticates, resolves the model to its upstream provider, enforces the
// ONE prepaid-balance gate, RESERVES the per-video budget, creates ONE upstream
// job, registers it in the shared in-pod store, and returns the OpenAI Sora
// video object with status "queued" IMMEDIATELY. Nothing is billed here — the
// debit lands EXACTLY ONCE on completion (poll or download), like the HTTP path.
func zapVideosGenerateHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	startVideoReaperOnce()

	if auth == "" {
		return object.BuildCloudResponse(401, nil, "authentication required")
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if isPublishableKey(token) {
		return object.BuildCloudResponse(403, nil, "publishable keys are read-only and cannot generate videos")
	}

	// Decode into the SAME struct the HTTP path uses (videos_api.go).
	var req videosGenerationsRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" || req.Prompt == "" {
		// Authenticate before reporting the client error: an invalid credential is
		// 401 regardless of body validity (never a probe-able 400), mirroring the
		// HTTP path's authenticate-then-400 ordering.
		if _, aerr := zapResolveUser(auth); aerr != nil {
			return object.BuildCloudResponse(401, nil, aerr.Error())
		}
		msg := "videos request requires a \"model\" field"
		if req.Model != "" {
			msg = "videos request requires a \"prompt\" field"
		}
		if jerr := json.Unmarshal(body, &req); jerr != nil {
			msg = "invalid request: " + jerr.Error()
		}
		return object.BuildCloudResponse(400, nil, msg)
	}

	// Auth → provider + user + upstream model. The IAM/JWT path resolves the org's
	// OWN provider (BYO seam) via GetModelProviderByNameForOrg(authUser.Owner).
	provider, authUser, upstreamModel, err := zapResolveAuth(auth, req.Model)
	if err != nil {
		return object.BuildCloudResponse(401, nil, err.Error())
	}
	if provider.Category != "Model" {
		return object.BuildCloudResponse(400, nil, fmt.Sprintf("Provider %s is not a model provider", provider.Name))
	}

	// Async video is per-user metered, and the job's billing subject is ALSO its
	// ownership key across the later poll/download. An anonymous/exempt principal
	// (no authUser, empty subject) has no subject to own or bill the job by —
	// reject rather than create an unownable, unbillable job.
	if authUser == nil {
		return object.BuildCloudResponse(401, nil, "Video generation requires an authenticated Hanzo Cloud account. Sign in and add credits to your wallet at "+object.PayURL(""))
	}
	subject := account.Payer(account.Credential{Owner: authUser.Owner, Name: authUser.Name}).Subject()
	if subject == "" {
		return object.BuildCloudResponse(401, nil, "Video generation requires an authenticated Hanzo Cloud account.")
	}

	// The ONE prepaid-balance gate, shared verbatim with the HTTP path.
	if gateErr := enforceBalanceGate(authUser, req.Model); gateErr != nil {
		return object.BuildCloudResponse(uint32(statusOf(gateErr)), nil, gateErr.Error())
	}
	isPremium := false
	if route := resolveModelRoute(req.Model); route != nil {
		isPremium = route.premium
	}

	startTime := time.Now().UTC()

	// Zen family: forward to the zen service, billed per clip at the discovered
	// price. This bypasses ai's own async job store — zen owns the upstream job.
	if provider.Type == "Zen" {
		return zapVideoServeZen(req.Model, body, authUser, isPremium, startTime)
	}

	// KMS secrets (env-first) so a kms:// ClientSecret is usable upstream.
	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
	}
	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if req.Model != "" {
		provider.SubType = req.Model
	}

	upstreamURL := videoUpstreamBase(provider)
	if upstreamURL == "" {
		return object.BuildCloudResponse(502, nil, "No upstream endpoint configured for provider: "+provider.Name)
	}

	// ── Balance reservation (the gate) ──────────────────────────────────
	// Reserve the exact per-video cost so a subject can't kick off jobs it can't
	// pay for. Settled with the ACTUAL cost on completion; released (settle 0) on
	// any early error below or by the reaper if the job is abandoned.
	hold, okReserve := reserveBudget(subject, videoCostCents(req.Model, 1))
	if !okReserve {
		return object.BuildCloudResponse(402, nil, object.InsufficientBalance(authUser.Owner, "video cost").Message)
	}

	// Create the upstream job — FAST: the async API returns a queued job id
	// immediately (the minutes-long generation runs upstream).
	createCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	upstreamID, upstreamStatus, err := model.CreateVideoDOAI(createCtx, upstreamURL, provider.ClientSecret, model.VideoGenRequest{
		UpstreamModel: provider.SubType,
		Prompt:        req.Prompt,
		Size:          req.Size,
		Seconds:       req.Seconds,
	})
	if err != nil {
		hold.settle(0) // nothing produced → release the reservation, bill nothing
		zapRecordVideoUsage(ctx, authUser, provider, req.Model, isPremium, 0, "error", err.Error(), startTime)
		return object.BuildCloudResponse(502, nil, "Video generation failed: "+err.Error())
	}

	job := &videoJob{
		id:         "video_" + util.GenerateUUID(),
		upstreamID: upstreamID,
		subject:    subject,
		owner:      authUser.Owner,
		name:       authUser.Name,
		userModel:  req.Model,
		isPremium:  isPremium,
		hold:       hold,
		createdAt:  time.Now(),
		startTime:  startTime,
		status:     normalizeVideoStatus(upstreamStatus),
	}
	if !videoJobs.add(job) {
		hold.settle(0) // at capacity → release the reservation before rejecting
		return object.BuildCloudResponse(429, nil, "Video generation is at capacity; please retry shortly.")
	}

	data, _ := json.Marshal(videoJobResponse(job))
	return object.BuildCloudResponse(200, data, "")
}

// ── videos.retrieve / GET /v1/videos/:id ──────────────────────────────────

// zapVideosRetrieveHandler is the native-cloud entrypoint for a status poll; the
// job id arrives in the body ({"id":"video_…"}).
func zapVideosRetrieveHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	id, msg := zapVideoIDFromBody(body)
	if msg != nil {
		return msg, nil
	}
	return zapVideoRetrieve(ctx, auth, id)
}

// zapVideoRetrieve is the native twin of ApiController.RetrieveVideo. It
// authenticates, verifies OWNERSHIP (caller subject == job subject, else 404),
// performs ONE upstream status poll, and — the first time the job is observed
// completed — settles the reservation with the actual cost and records the
// billable usage event EXACTLY ONCE.
func zapVideoRetrieve(ctx context.Context, auth, id string) (*zap.Message, error) {
	job, provider, authUser, errMsg, ok := zapResolveOwnedVideoJob(auth, id)
	if !ok {
		return errMsg, nil
	}

	// A job already terminal in this pod is authoritative — re-polling is wasted
	// work (and a failed upstream job may already be GC'd upstream).
	if isTerminalVideoStatus(job.snapshotStatus()) {
		data, _ := json.Marshal(videoJobResponse(job))
		return object.BuildCloudResponse(200, data, "")
	}

	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	upstreamURL := videoUpstreamBase(provider)
	status, errStr, err := model.RetrieveVideoDOAI(pollCtx, upstreamURL, provider.ClientSecret, job.upstreamID)
	if err != nil {
		return object.BuildCloudResponse(502, nil, "Video status poll failed: "+err.Error())
	}

	switch normalizeVideoStatus(status) {
	case "completed":
		// First completion → settle the reservation with the real cost and record
		// the single billable usage event. Idempotent across repeat polls.
		if job.markCompleted(videoCostCents(job.userModel, 1)) {
			zapRecordVideoUsage(ctx, authUser, provider, job.userModel, job.isPremium, 1, "success", "", job.startTime)
		}
	case "failed":
		if job.markFailed("failed") {
			zapRecordVideoUsage(ctx, authUser, provider, job.userModel, job.isPremium, 0, "error", errStr, job.startTime)
		}
		job.setFailureReason(errStr)
	default:
		job.setStatus(normalizeVideoStatus(status))
	}

	data, _ := json.Marshal(videoJobResponse(job))
	return object.BuildCloudResponse(200, data, "")
}

// ── videos.content / GET /v1/videos/:id/content ───────────────────────────

// zapVideosContentHandler is the native-cloud entrypoint for a download; the job
// id arrives in the body ({"id":"video_…"}).
func zapVideosContentHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	id, msg := zapVideoIDFromBody(body)
	if msg != nil {
		return msg, nil
	}
	return zapVideoContent(ctx, auth, id)
}

// zapVideoContent is the native twin of ApiController.VideoContent. It
// authenticates + ownership-checks the caller, proxies the upstream /content
// endpoint (bounded by the download concurrency ceiling) and returns the raw MP4
// bytes. A successful download also bills the job once (idempotent with the poll
// path via job.markCompleted). The raw video bytes are returned in the response
// body; the native-cloud response has no header channel, so a gateway projection
// (with Content-Type/Content-Disposition) rides on Integrate's path-aware
// gateway wiring.
func zapVideoContent(ctx context.Context, auth, id string) (*zap.Message, error) {
	job, provider, authUser, errMsg, ok := zapResolveOwnedVideoJob(auth, id)
	if !ok {
		return errMsg, nil
	}

	// Memory guard: bound concurrent MP4 relays so a download flood can't OOM this
	// single-replica pod. A full pod fails fast with 429 (retryable).
	if !tryAcquireVideoSlot() {
		return object.BuildCloudResponse(429, nil, "Video download is at capacity; please retry shortly.")
	}
	defer releaseVideoSlot()

	dlCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	upstreamURL := videoUpstreamBase(provider)
	data, _, err := model.DownloadVideoBytesDOAI(dlCtx, upstreamURL, provider.ClientSecret, job.upstreamID)
	if err != nil {
		return object.BuildCloudResponse(502, nil, "Video download failed: "+err.Error())
	}

	// A successful download is proof of a completed video — bill once (covers a
	// client that skips polling). Idempotent with the poll path's completion.
	if job.markCompleted(videoCostCents(job.userModel, 1)) {
		zapRecordVideoUsage(ctx, authUser, provider, job.userModel, job.isPremium, 1, "success", "", job.startTime)
	}

	return object.BuildCloudResponse(200, data, "")
}

// ── Ownership + helpers ────────────────────────────────────────────────────

// zapVideoIDFromBody extracts the job id from a native-cloud poll/download body.
// A missing id is a 400 (returned as the *zap.Message); ok is signalled by a nil
// message.
func zapVideoIDFromBody(body []byte) (string, *zap.Message) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &params); err != nil {
		msg, _ := object.BuildCloudResponse(400, nil, "invalid request: "+err.Error())
		return "", msg
	}
	if params.ID == "" {
		msg, _ := object.BuildCloudResponse(400, nil, "request requires an \"id\" field")
		return "", msg
	}
	return params.ID, nil
}

// zapResolveOwnedVideoJob is the native twin of ApiController.resolveOwnedVideoJob:
// it looks up the job, authenticates the caller, and enforces ownership — the
// caller's billing subject MUST equal the job's. A missing job OR a subject
// mismatch is reported as an identical 404, so a caller can never confirm the
// existence of another user's job. It re-resolves (and KMS-resolves) the provider
// so the provider key is fresh and never stored. On any failure it returns the
// response message and ok=false.
func zapResolveOwnedVideoJob(auth, id string) (*videoJob, *object.Provider, *iam.User, *zap.Message, bool) {
	job, found := videoJobs.get(id)
	if !found {
		// Authenticate first so an invalid credential is 401 (never a probe-able
		// 404 that distinguishes "bad key" from "no such job").
		if _, aerr := zapResolveUser(auth); aerr != nil {
			msg, _ := object.BuildCloudResponse(401, nil, aerr.Error())
			return nil, nil, nil, msg, false
		}
		msg, _ := object.BuildCloudResponse(404, nil, "No such video job.")
		return nil, nil, nil, msg, false
	}

	provider, authUser, _, err := zapResolveAuth(auth, job.userModel)
	if err != nil {
		msg, _ := object.BuildCloudResponse(401, nil, err.Error())
		return nil, nil, nil, msg, false
	}

	subject := ""
	if authUser != nil {
		subject = account.Payer(account.Credential{Owner: authUser.Owner, Name: authUser.Name}).Subject()
	}
	// Ownership: a non-empty subject that matches the job's. An empty subject
	// (anonymous/exempt) owns nothing. A mismatch is a 404 (never reveal the job).
	if subject == "" || subject != job.subject {
		msg, _ := object.BuildCloudResponse(404, nil, "No such video job.")
		return nil, nil, nil, msg, false
	}

	// KMS secrets (env-first) so the re-resolved provider's ClientSecret is usable.
	if err := object.ResolveProviderSecret(provider); err != nil {
		log.Error("ZAP: KMS resolve %s: %v", provider.Name, err)
	}

	return job, provider, authUser, nil, true
}

// zapRecordVideoUsage records a single video-generation usage event for billing +
// observability, mirroring recordVideoUsage (videos_api.go). Only successful
// calls are billed (recordUsage filters error status); the trace is emitted
// either way. No client IP is available on the ZAP path.
func zapRecordVideoUsage(ctx context.Context, authUser *iam.User, provider *object.Provider, userModel string, isPremium bool, videoCount int, status, errMsg string, startTime time.Time) {
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner:        authUser.Owner,
		User:         authUser.Owner + "/" + authUser.Name,
		Organization: authUser.Owner,
		Model:        userModel,
		Provider:     provider.Name,
		VideoCount:   videoCount,
		Currency:     "USD",
		Premium:      isPremium,
		Status:       status,
		ErrorMsg:     errMsg,
		RequestID:    util.GenerateUUID(),
	}
	recordUsage(rec)
	recordTrace(ctx, rec, startTime)
}

// ── Zen video forwarding (buffered, no http.ResponseWriter) ────────────────
//
// The pure-ZAP mirror of serveZenMedia/pipeZenMedia (zen_media.go) for the video
// verb: reserve the per-clip cost, forward to zen, relay the upstream bytes,
// settle at the discovered price. One buffered response — no writer to hold. A
// uniquely-named helper (not the shared zapServeZenMedia) so this group's file is
// self-contained; consolidating the per-verb Zen forwards into one shared helper
// is an Integrate reconciliation once the in-flight media groups land.
func zapVideoServeZen(mdl string, rawBody []byte, authUser *iam.User, isPremium bool, start time.Time) (*zap.Message, error) {
	const units = 1 // the async /videos API is one-video-per-job

	var hold *budgetHold
	if authUser != nil {
		if zm, ok := zenFam.lookup(mdl); ok {
			subject := account.Payer(account.Credential{Owner: authUser.Owner, Name: authUser.Name}).Subject()
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
	reqID := util.GenerateUUID()

	hctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()

	hreq, err := http.NewRequestWithContext(hctx, http.MethodPost, prov.ProviderUrl+"/v1/videos/generations", bytes.NewReader(rawBody))
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
		zapRecordVideoZenUsage(mdl, authUser, isPremium, reqID, units, start, hold, "error", err.Error())
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

	zapRecordVideoZenUsage(mdl, authUser, isPremium, reqID, units, start, hold, "success", "")
	return object.BuildCloudResponse(200, b, "")
}

// zapRecordVideoZenUsage settles the hold at the discovered per-clip price and
// records the served (or failed) call, mirroring recordZenMediaUsage. A model
// absent from the cache settles to zero (released).
func zapRecordVideoZenUsage(mdl string, authUser *iam.User, isPremium bool, reqID string, units int, start time.Time, hold *budgetHold, status, errMsg string) {
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
	recordUsage(rec)
	recordTrace(context.Background(), rec, start)
}
