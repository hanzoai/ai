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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
)

// This file completes the OpenAI-compatible surface alongside chat, embeddings,
// images, and audio with the ASYNC text-to-video API — modelled on OpenAI Sora:
//
//	POST /v1/videos/generations   → creates a job, returns its id immediately
//	GET  /v1/videos/{id}          → polls the job's status
//	GET  /v1/videos/{id}/content  → downloads the finished MP4
//
// Async is the CORRECT shape here (not a workaround): a generation takes minutes,
// and the old synchronous handler held the request open for its whole duration
// (~104s), which timed out the console /ai bearer proxy and returned a 502 to the
// browser WHILE the generation actually succeeded. Splitting the lifecycle into
// three fast requests removes the hold entirely — nothing waits minutes.
//
// It rides the SAME authentication, model-routing, provider-resolution, and
// per-user balance machinery as ImagesGenerations — one auth+routing policy, one
// billing ledger. The reservation is taken at CREATE (the balance gate) and
// settled EXACTLY ONCE on completion (observed during a poll or the download) —
// video is expensive, so the debit is deferred to real completion and never
// double-charged (see videoJob.markCompleted). The provider is re-resolved on
// every request, so the provider key is never stored, and each request re-runs
// the auth+ownership check (a caller can only touch THEIR OWN job).

// ── Video download concurrency ceiling ───────────────────────────────────────
//
// With the async split, the pod no longer buffers a video during generation (that
// now runs upstream). The ONLY memory-heavy path left is /content, which reads
// the finished MP4 into memory to relay it. This pod is single-replica and also
// serves chat/image/RAG, so a flood of concurrent downloads could still exhaust
// its memory. videoSem bounds concurrent downloads; create/poll are cheap and
// ungated. A full pod fails a download fast with 429 rather than queueing.

// defaultVideoMaxConcurrent is the built-in ceiling on simultaneous video
// downloads, matched to the pod's memory budget (each download is capped at
// model.doaiVideoContentMax). Small by design: a handful of concurrent MP4 relays
// is plenty and keeps the pod safe.
const defaultVideoMaxConcurrent = 2

// videoMaxConcurrent resolves the ceiling, overridable via VIDEO_MAX_CONCURRENT
// for a pod provisioned with more headroom. A non-positive / unparseable value
// falls back to the safe default (never 0 — that would deadlock every download).
func videoMaxConcurrent() int {
	if v := strings.TrimSpace(os.Getenv("VIDEO_MAX_CONCURRENT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultVideoMaxConcurrent
}

// videoSem admits at most videoMaxConcurrent() concurrent downloads.
var videoSem = make(chan struct{}, videoMaxConcurrent())

// tryAcquireVideoSlot takes one download slot WITHOUT blocking, returning false
// when the pod is already at capacity (the handler then answers 429).
func tryAcquireVideoSlot() bool {
	select {
	case videoSem <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseVideoSlot returns a previously acquired slot. It must be called exactly
// once per successful tryAcquireVideoSlot (via defer).
func releaseVideoSlot() { <-videoSem }

// videosGenerationsRequest is the /v1/videos/generations create body. Only model
// + prompt are required; size/seconds are optional pass-throughs. (n is not
// accepted: the async /videos API is one-video-per-job, matching OpenAI Sora.)
type videosGenerationsRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Size    string `json:"size"`
	Seconds int    `json:"seconds"`
}

// VideosGenerations implements POST /v1/videos/generations — the ASYNC create.
//
// Body: {"model": "...", "prompt": "...", "size"?: "1280x720", "seconds"?: int}
//
// It authenticates the caller, resolves the model to its upstream provider via
// the shared routing table (zen3-video* / wan2-2-t2v-a14b → the spark-video
// backend), reserves the per-video budget (the balance gate), creates ONE
// upstream job, registers it in the in-pod store, and returns the OpenAI-shaped
// video object with status "queued" IMMEDIATELY. The client then polls
// GET /v1/videos/{id} and downloads GET /v1/videos/{id}/content. Nothing is
// billed here — the debit lands on completion.
//
// @Title VideosGenerations
// @Tag OpenAI Compatible API
// @Description OpenAI Sora-style async text-to-video: create a generation job.
// @router /videos/generations [post]
func (c *ApiController) VideosGenerations() {
	startVideoReaperOnce()

	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}

	var req videosGenerationsRequest
	badReq := ""
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", err.Error())
	} else if req.Model == "" {
		badReq = "videos request requires a \"model\" field"
	} else if req.Prompt == "" {
		badReq = "videos request requires a \"prompt\" field"
	}
	if badReq != "" {
		// Authenticate before reporting the client error: an invalid credential is
		// 401 regardless of body validity (never a probe-able 200/400).
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, badReq)
		return
	}

	startTime := time.Now().UTC()
	orgId := c.GetOrg()

	provider, authUser, upstreamModel, isPremium, err := c.authResolveProvider(token, req.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	if provider.Category != "Model" {
		c.ResponseError(fmt.Sprintf("Provider %s is not a model provider", provider.Name))
		return
	}

	// Async video is per-user metered, and the job's billing subject is ALSO its
	// ownership key across the later poll/download requests. So a billing identity
	// is required: an anonymous/exempt principal (no authUser, empty subject) has
	// no subject to own or bill the job by. Reject it rather than create an
	// unownable, unbillable job.
	if authUser == nil {
		c.ResponseAuthError(billingError("Video generation requires an authenticated Hanzo Cloud account. Sign in and add credits to your wallet at %s", object.PayURL(c.Host(), "")))
		return
	}
	ledger := c.billingOrg(authUser)
	subject := authUser.PayerSubject(ledger)
	if subject == "" {
		c.ResponseAuthError(billingError("Video generation requires an authenticated Hanzo Cloud account."))
		return
	}

	// Zen family: forward to the zen service, billed per clip at the discovered
	// price. This bypasses ai's own async job store — zen (and its self-hosted
	// engine upstream) owns the video job. If the upstream returns a job to poll,
	// the client polls zen directly; ai does not proxy the poll/download (a
	// follow-on for the async engine path).
	if provider.Type == "Zen" {
		c.serveZenMedia("videos/generations", req.Model, c.Body(), 1, orgId, authUser, isPremium, startTime)
		return
	}

	if upstreamModel != "" {
		provider.SubType = upstreamModel
	} else if req.Model != "" {
		provider.SubType = req.Model
	}

	upstreamURL := videoUpstreamBase(provider)
	if upstreamURL == "" {
		c.ResponseError("No upstream endpoint configured for provider: " + provider.Name)
		return
	}

	// ── Balance reservation (the gate) ──────────────────────────────────
	// Reserve the exact per-video cost so a subject can't kick off jobs it can't
	// pay for (and so concurrent creates can't over-commit a balance the async
	// debit hasn't applied yet). Settled with the ACTUAL cost when the job
	// completes; released (settle 0) on any early error below or by the reaper if
	// the job is abandoned.
	hold, okReserve := reserveBudget(subject, videoCostCents(req.Model, 1))
	if !okReserve {
		c.ResponseAuthError(billingError("%s", object.InsufficientBalance(c.Host(), ledger, "video cost").Message))
		return
	}

	// Create the upstream job. This is FAST — the async API returns a queued job
	// id immediately (the minutes-long generation runs upstream). A short timeout
	// bounds a wedged create; the request context cancels it if the client leaves.
	ctx, cancel := context.WithTimeout(c.Context(), 60*time.Second)
	defer cancel()

	upstreamID, upstreamStatus, err := model.CreateVideoDOAI(ctx, upstreamURL, provider.ClientSecret, model.VideoGenRequest{
		UpstreamModel: provider.SubType,
		Prompt:        req.Prompt,
		Size:          req.Size,
		Seconds:       req.Seconds,
	})
	if err != nil {
		hold.settle(0) // nothing produced → release the reservation, bill nothing
		c.recordVideoUsage(authUser, provider, req.Model, isPremium, 0, "error", err.Error(), startTime)
		c.ResponseError(fmt.Sprintf("Video generation failed: %s", err.Error()))
		return
	}

	job := &videoJob{
		id:         "video_" + uuid.NewString(),
		upstreamID: upstreamID,
		subject:    subject,
		owner:      ledger,
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
		c.ResponseErrorWithStatus(http.StatusTooManyRequests,
			"Video generation is at capacity; please retry shortly.")
		return
	}

	c.jsonResponse(videoJobResponse(job))
}

// RetrieveVideo implements GET /v1/videos/{id} — poll a job's status.
//
// It authenticates the caller, verifies they OWN the job (the caller's billing
// subject must equal the job's), performs ONE upstream status poll, and — the
// first time the job is observed completed — settles the reservation with the
// actual cost and records the billable usage event (exactly once). Returns the
// OpenAI-shaped video object.
//
// @Title RetrieveVideo
// @Tag OpenAI Compatible API
// @Description Retrieve the status of an async video generation job.
// @router /videos/:id [get]
func (c *ApiController) RetrieveVideo() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	id := c.Param("id")

	job, provider, authUser, ok := c.resolveOwnedVideoJob(token, id)
	if !ok {
		return
	}

	// A job already settled terminal in this pod needs no further upstream poll —
	// its status is authoritative and re-polling a completed/failed job is wasted
	// work (and a failed upstream job may have already been GC'd upstream).
	if isTerminalVideoStatus(job.snapshotStatus()) {
		c.jsonResponse(videoJobResponse(job))
		return
	}

	ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
	defer cancel()

	upstreamURL := videoUpstreamBase(provider)
	status, errMsg, err := model.RetrieveVideoDOAI(ctx, upstreamURL, provider.ClientSecret, job.upstreamID)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Video status poll failed: %s", err.Error()))
		return
	}

	switch normalizeVideoStatus(status) {
	case "completed":
		// First completion → settle the reservation with the real cost and record
		// the single billable usage event. Idempotent across repeat polls.
		if job.markCompleted(videoCostCents(job.userModel, 1)) {
			c.recordVideoUsage(authUser, provider, job.userModel, job.isPremium, 1, "success", "", job.startTime)
		}
	case "failed":
		if job.markFailed("failed") {
			// A failed job bills nothing (recordUsage filters non-success), but the
			// trace is emitted once for observability.
			c.recordVideoUsage(authUser, provider, job.userModel, job.isPremium, 0, "error", errMsg, job.startTime)
		}
		job.setFailureReason(errMsg)
	default:
		job.setStatus(normalizeVideoStatus(status))
	}

	c.jsonResponse(videoJobResponse(job))
}

// VideoContent implements GET /v1/videos/{id}/content — download the finished MP4.
//
// It authenticates + ownership-checks the caller, then proxies the upstream
// /content endpoint (bounded by the download concurrency ceiling) and streams the
// raw video bytes back inline. A successful download also bills the job once (for
// the client that downloads without first polling to completion) — idempotent
// with the poll path via job.markCompleted.
//
// @Title VideoContent
// @Tag OpenAI Compatible API
// @Description Download the finished MP4 of a completed video generation job.
// @router /videos/:id/content [get]
func (c *ApiController) VideoContent() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	id := c.Param("id")

	job, provider, authUser, ok := c.resolveOwnedVideoJob(token, id)
	if !ok {
		return
	}

	// Memory guard: bound concurrent MP4 relays so a download flood can't OOM this
	// single-replica pod. A full pod fails fast with 429 (retryable).
	if !tryAcquireVideoSlot() {
		c.ResponseErrorWithStatus(http.StatusTooManyRequests,
			"Video download is at capacity; please retry shortly.")
		return
	}
	defer releaseVideoSlot()

	ctx, cancel := context.WithTimeout(c.Context(), 120*time.Second)
	defer cancel()

	upstreamURL := videoUpstreamBase(provider)
	data, mime, err := model.DownloadVideoBytesDOAI(ctx, upstreamURL, provider.ClientSecret, job.upstreamID)
	if err != nil {
		c.ResponseError(fmt.Sprintf("Video download failed: %s", err.Error()))
		return
	}

	// A successful download is proof of a completed video — bill once (covers a
	// client that skips polling). Idempotent with RetrieveVideo's completion path.
	if job.markCompleted(videoCostCents(job.userModel, 1)) {
		c.recordVideoUsage(authUser, provider, job.userModel, job.isPremium, 1, "success", "", job.startTime)
	}

	if mime == "" {
		mime = "video/mp4"
	}
	c.SetHeader("Content-Type", mime)
	c.SetHeader("Content-Disposition", "inline; filename="+job.id+".mp4")
	if err := c.Bytes(http.StatusOK, data); err != nil {
		responseError(c.Ctx, http.StatusInternalServerError, err.Error())
		return
	}
}

// resolveOwnedVideoJob looks up the job, authenticates the caller, and enforces
// ownership — the caller's billing subject MUST equal the job's. It re-resolves
// the provider (so the provider key is fresh and never stored) and returns it for
// the caller to proxy with. A missing job OR a subject mismatch is reported as
// 404 with an identical message, so a caller can never confirm the existence of
// another user's job. On any failure it writes the response and returns ok=false.
func (c *ApiController) resolveOwnedVideoJob(token, id string) (*videoJob, *object.Provider, *iam.User, bool) {
	// A publishable key is refused here for the reason it is refused at the
	// generation door: it names an ORG and no person, so the ownership test below
	// collapses to "same tenant" and every job in the org answers to it. A key that
	// ships in a page would read whatever anyone in that org had generated.
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return nil, nil, nil, false
	}

	job, found := videoJobs.get(id)
	if !found {
		// Authenticate first so an invalid credential is 401 (never a probe-able
		// 404 that distinguishes "bad key" from "no such job").
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return nil, nil, nil, false
		}
		c.ResponseErrorWithStatus(http.StatusNotFound, "No such video job.")
		return nil, nil, nil, false
	}

	orgId := c.GetOrg()
	provider, authUser, _, _, err := c.authResolveProvider(token, job.userModel, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return nil, nil, nil, false
	}

	subject := ""
	if authUser != nil {
		subject = authUser.PayerSubject(c.billingOrg(authUser))
	}
	// Ownership: a non-empty subject that matches the job's. An empty subject
	// (anonymous/exempt) owns nothing. A mismatch is a 404 (never reveal the job).
	if subject == "" || subject != job.subject {
		c.ResponseErrorWithStatus(http.StatusNotFound, "No such video job.")
		return nil, nil, nil, false
	}

	return job, provider, authUser, true
}

// videoJobResponse projects a job into the OpenAI Sora-shaped video object the
// client sees. It never exposes the upstream id, provider, or key.
func videoJobResponse(job *videoJob) map[string]interface{} {
	job.mu.Lock()
	status := job.status
	failure := job.failureReason
	job.mu.Unlock()

	resp := map[string]interface{}{
		"id":         job.id,
		"object":     "video",
		"model":      job.userModel,
		"status":     status,
		"created_at": job.createdAt.Unix(),
	}
	switch status {
	case "completed":
		resp["progress"] = 100
	case "failed":
		resp["progress"] = 0
		errObj := map[string]interface{}{"message": "Video generation failed."}
		if failure != "" {
			errObj["message"] = failure
		}
		resp["error"] = errObj
	default:
		resp["progress"] = 0
	}
	return resp
}

// videoUpstreamBase returns the provider's base URL for the async video API (the
// provider's ProviderUrl, e.g. https://spark-video.hanzo.ai/v1). It reuses
// endpoint so provider URL handling lives in exactly one place; the async client
// appends /videos itself, so the empty path yields the clean base with the
// provider's /v1 normalization applied.
func videoUpstreamBase(provider *object.Provider) string {
	base := upstream.Endpoint(provider, "")
	// endpoint returns "<base>/" for the empty path; trim the trailing slash so
	// the async client's "/videos" join is clean.
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base
}

// recordVideoUsage records a single video-generation usage event for billing +
// observability, mirroring recordImageUsage. videoCount drives the per-video
// cost; only successful calls are billed (recordUsage filters error status), but
// the trace is emitted either way.
func (c *ApiController) recordVideoUsage(authUser *iam.User, provider *object.Provider, userModel string, isPremium bool, videoCount int, status, errMsg string, startTime time.Time) {
	if authUser == nil {
		return
	}
	rec := &usageRecord{
		Owner:        c.billingOrg(authUser),
		Organization: authUser.Owner,
		Model:        userModel,
		Provider:     provider.Name,
		Origin:       provider.Origin(),
		VideoCount:   videoCount,
		Currency:     "USD",
		Premium:      isPremium,
		Status:       status,
		ErrorMsg:     errMsg,
		ClientIP:     c.Fiber().IP(),
		RequestID:    uuid.NewString(),
	}
	rec.bind(c.Context(), authUser)
	recordUsage(rec)
	recordTrace(c.Context(), rec, startTime)
}
