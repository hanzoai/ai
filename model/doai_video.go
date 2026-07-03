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

package model

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/ai/proxy"
)

// DigitalOcean serves its text-to-video model (wan2-2-t2v-a14b) through the
// OpenAI Sora-style ASYNCHRONOUS /v1/videos API — NOT the fal /async-invoke
// image API (that endpoint explicitly rejects the video model with "this model
// is not a async image generation model"). This file is the ONE client for that
// API. The /v1/videos/generations handler (controllers/videos_api.go) drives it.
// It presents a synchronous result (create → poll → download) so the handler can
// return an OpenAI-compatible response.
//
// Contract (base = provider ProviderUrl, e.g. https://inference.do-ai.run/v1):
//
//	create    POST {base}/videos
//	          {"model":"wan2-2-t2v-a14b","prompt":"…"}
//	          → 202 {"id":"video_…","object":"video","status":"queued"}
//	poll      GET  {base}/videos/{id}
//	          → {"id","model","object":"video",
//	             "status":"queued"|"in_progress"|"completed"|"failed",
//	             "output":null,"error":null|{…}}
//	download  GET  {base}/videos/{id}/content
//	          → Content-Type video/mp4, the raw MP4 bytes (ONLY valid once the
//	            job is "completed"; while "in_progress" this returns a tiny JSON
//	            placeholder, so we always poll to completion first).
//
// The Authorization is "Bearer {apiKey}" — the SAME do-ai key the chat/image
// routes resolve (never logged). The completed video bytes live ONLY behind the
// key-authenticated /content endpoint, so we download them here and hand the
// handler base64 (self-contained, no secret ever leaves the server).

const (
	// doaiVideoPollInterval is the delay between status polls. Text-to-video is
	// minutes, not seconds; a 3s interval keeps latency reasonable without
	// hammering the upstream (which rate-limits aggressively).
	doaiVideoPollInterval = 3 * time.Second

	// doaiVideoMaxWait bounds the total create→complete wall time. wan2-2-t2v
	// returns a short clip in ~60-120s; 300s is a generous ceiling that still
	// fails a wedged job rather than hanging the request forever. A timeout is a
	// RETRYABLE error (never a partial/silent success).
	doaiVideoMaxWait = 300 * time.Second

	// doaiVideoContentMax caps how many bytes we download from /content. Observed
	// real wan2-2-t2v clips are ~0.4 MB; 16 MiB is a hard safety ceiling (~40× the
	// observed size — ample headroom for a longer/higher-res legitimate clip) that
	// still stops a runaway/oversized response from exhausting memory. It is set
	// deliberately low (was 64 MiB) because this pod is single-replica and also
	// serves chat/image/RAG: at n videos each up to this cap, plus the base64 blow-
	// up and the marshalled response copy, the per-request memory is bounded, and
	// the pod-wide concurrency ceiling (controllers.videoSem) bounds the aggregate.
	doaiVideoContentMax = 16 << 20
)

// GeneratedVideo is one video from a generation call. Exactly one of URL or
// B64JSON is set. do-ai returns the bytes behind a key-authed /content endpoint,
// so this client fills B64JSON (base64 MP4); URL is reserved for the future case
// where an upstream returns a publicly fetchable hosted URL.
type GeneratedVideo struct {
	URL     string
	B64JSON string
	// MimeType is the content type of the video bytes (e.g. "video/mp4"), so the
	// handler can surface it. Empty defaults to video/mp4.
	MimeType string
}

// VideoGenResult is the structured result of a video generation: the videos and
// (implicitly, via len) the count actually produced, for billing via
// controllers.videoCostCents.
type VideoGenResult struct {
	Videos []GeneratedVideo
}

// VideoGenRequest is the normalized, provider-agnostic video request the model
// layer accepts. N defaults to 1; the OpenAI /videos API produces one video per
// create, so N>1 is realized by N sequential creates.
type VideoGenRequest struct {
	// UpstreamModel is the do-ai model id (e.g. "wan2-2-t2v-a14b").
	UpstreamModel string
	Prompt        string
	// N is the number of videos to produce; clamped to [1, doaiVideoMaxN].
	N int
	// Size is an optional OpenAI-style "WxH" hint passed through when set.
	Size string
	// Seconds is an optional clip-length hint (upstream default when 0).
	Seconds int
}

// doaiVideoMaxN caps the number of videos per request. Video generation is
// expensive and minutes-long, so the ceiling is deliberately lower than the
// image ceiling (10). Mirrored by the handler's clamp so cost math is bounded.
const doaiVideoMaxN = 4

// GenerateVideoDOAI runs a synchronous text-to-video generation against
// DigitalOcean's async /v1/videos API: create, poll to completion, download the
// bytes. It produces req.N videos by issuing that lifecycle N times (the API is
// one-video-per-create). baseURL is the provider's ProviderUrl (…/v1); apiKey is
// the resolved do-ai key. It never logs the key. Returns the produced videos, or
// an error (a failed/timed-out job is an error, never a partial success) — but
// if at least one video was produced before a later one failed, it returns the
// videos it has so the caller bills only for what it delivers.
func GenerateVideoDOAI(ctx context.Context, baseURL, apiKey string, req VideoGenRequest) (*VideoGenResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("video generation requires a non-empty prompt")
	}
	if req.UpstreamModel == "" {
		return nil, fmt.Errorf("video generation requires an upstream model id")
	}

	n := req.N
	if n < 1 {
		n = 1
	}
	if n > doaiVideoMaxN {
		n = doaiVideoMaxN
	}

	base := strings.TrimRight(baseURL, "/")
	client := proxy.ProxyHttpClient
	if client == nil {
		client = http.DefaultClient
	}

	result := &VideoGenResult{Videos: make([]GeneratedVideo, 0, n)}
	for i := 0; i < n; i++ {
		vid, err := generateOneVideoDOAI(ctx, client, base, apiKey, req)
		if err != nil {
			// A canceled / expired context means the CALLER is gone — a client
			// disconnect, or an upstream gateway/ingress that timed out and closed
			// the connection. There is no one to deliver to, so return the context
			// error even if earlier videos completed: the handler then settles the
			// hold to 0 and bills NOTHING (never a charged-but-undelivered clip).
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			// Genuine upstream failure (not a context error): if we already produced
			// at least one video, deliver those rather than discarding paid-for work;
			// the handler bills only len(result.Videos) (<= n). Otherwise surface the
			// error so the hold releases and nothing is billed.
			if len(result.Videos) > 0 {
				return result, nil
			}
			return nil, err
		}
		result.Videos = append(result.Videos, *vid)
	}
	return result, nil
}

// generateOneVideoDOAI runs one create → poll → download lifecycle.
func generateOneVideoDOAI(ctx context.Context, client *http.Client, base, apiKey string, req VideoGenRequest) (*GeneratedVideo, error) {
	id, err := doaiVideoCreate(ctx, client, base, apiKey, req)
	if err != nil {
		return nil, err
	}
	if err := doaiVideoWait(ctx, client, base, apiKey, id); err != nil {
		return nil, err
	}
	return doaiVideoDownload(ctx, client, base, apiKey, id)
}

// doaiVideoCreateBody is the OpenAI /videos create request. prompt+model are
// always sent; the size/seconds hints are omitempty so a field the model rejects
// is never transmitted.
type doaiVideoCreateBody struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Size    string `json:"size,omitempty"`
	Seconds int    `json:"seconds,omitempty"`
}

// doaiVideoRecord is the OpenAI video object returned by create and poll.
type doaiVideoRecord struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// doaiVideoCreate POSTs the create request and returns the video id.
func doaiVideoCreate(ctx context.Context, client *http.Client, base, apiKey string, req VideoGenRequest) (string, error) {
	body := doaiVideoCreateBody{
		Model:   req.UpstreamModel,
		Prompt:  req.Prompt,
		Size:    req.Size,
		Seconds: req.Seconds,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal video request: %w", err)
	}

	resp, err := doaiVideoDo(ctx, client, http.MethodPost, base+"/videos", apiKey, payload)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", videoUpstreamError("video create", resp.StatusCode, raw)
	}

	var rec doaiVideoRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return "", fmt.Errorf("decode video create response: %w", err)
	}
	if rec.ID == "" {
		return "", fmt.Errorf("video create returned no id")
	}
	return rec.ID, nil
}

// doaiVideoWait polls the job status until it completes, fails, or the deadline
// (doaiVideoMaxWait / ctx) elapses. A timeout is surfaced as a retryable error.
func doaiVideoWait(ctx context.Context, client *http.Client, base, apiKey, id string) error {
	deadline := time.Now().Add(doaiVideoMaxWait)
	statusURL := base + "/videos/" + id

	for {
		resp, err := doaiVideoDo(ctx, client, http.MethodGet, statusURL, apiKey, nil)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return videoUpstreamError("video status poll", resp.StatusCode, raw)
		}

		var rec doaiVideoRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("decode video status: %w", err)
		}

		switch strings.ToLower(rec.Status) {
		case "completed", "complete", "succeeded", "success":
			return nil
		case "failed", "error", "canceled", "cancelled":
			if rec.Error != nil && rec.Error.Message != "" {
				return fmt.Errorf("video generation %s: %s", strings.ToLower(rec.Status), videoSafeReason([]byte(rec.Error.Message)))
			}
			return fmt.Errorf("video generation %s", strings.ToLower(rec.Status))
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("video generation timed out after %s (last status: %s); retry", doaiVideoMaxWait, rec.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(doaiVideoPollInterval):
		}
	}
}

// doaiVideoDownload fetches the completed video bytes from /content and returns
// them base64-encoded. The bytes live ONLY behind this key-authenticated
// endpoint, so we download server-side and never expose the do-ai URL/key.
func doaiVideoDownload(ctx context.Context, client *http.Client, base, apiKey, id string) (*GeneratedVideo, error) {
	resp, err := doaiVideoDo(ctx, client, http.MethodGet, base+"/videos/"+id+"/content", apiKey, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, doaiErrBodyMax+1))
		return nil, videoUpstreamError("video download", resp.StatusCode, raw)
	}

	// A completed job must return video media. Accept ONLY a video/* content type,
	// a generic binary stream (application/octet-stream), or a missing header (some
	// CDNs omit it on binary downloads) — an allowlist, not allow-by-default. Any
	// other EXPLICIT type — notably the application/json placeholder the async API
	// returns while the job is still in_progress — means the payload is NOT a video,
	// so reject it rather than base64-encode a non-video document and hand it back
	// as a clip.
	mime := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if i := strings.IndexByte(mime, ';'); i >= 0 { // drop any "; charset=…" parameter
		mime = strings.TrimSpace(mime[:i])
	}
	if mime != "" && mime != "application/octet-stream" && !strings.HasPrefix(mime, "video/") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, doaiErrBodyMax+1))
		return nil, fmt.Errorf("video content not ready (upstream returned %q, not video media): %s", mime, videoSafeReason(raw))
	}
	// Normalize the surfaced media type: a bare stream / missing header defaults to
	// video/mp4 (what do-ai serves); an explicit video/* type is kept as-is.
	returnMime := mime
	if returnMime == "" || returnMime == "application/octet-stream" {
		returnMime = "video/mp4"
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, doaiVideoContentMax+1))
	if err != nil {
		return nil, fmt.Errorf("read video content: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("video generation returned empty content")
	}
	if int64(len(data)) > doaiVideoContentMax {
		return nil, fmt.Errorf("video content exceeds %d bytes", doaiVideoContentMax)
	}

	return &GeneratedVideo{
		B64JSON:  base64.StdEncoding.EncodeToString(data),
		MimeType: returnMime,
	}, nil
}

// doaiVideoDo builds and sends one authenticated request. The apiKey rides the
// Authorization header only (never logged, never in the URL or body).
func doaiVideoDo(ctx context.Context, client *http.Client, method, url, apiKey string, body []byte) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("build video request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("video request to upstream failed: %w", err)
	}
	return resp, nil
}

// secretLikePattern matches credential-shaped substrings an upstream error body
// might echo back: an Authorization header, a Bearer token, or an sk-/hk- API key.
// They are redacted before any upstream reason is embedded in an error we surface
// to the caller, so a misbehaving upstream can never turn our error path into a
// key-disclosure oracle.
var secretLikePattern = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?\S+|bearer\s+\S+|\b[sh]k-[A-Za-z0-9._\-]{6,})`)

// videoSafeReason produces a short, secret-scrubbed reason from an upstream error
// body: it redacts credential-shaped substrings BEFORE truncation (so a secret
// straddling the length cap can't partially leak) and then length-caps the result
// via shortUpstreamErr.
func videoSafeReason(raw []byte) string {
	return shortUpstreamErr(secretLikePattern.ReplaceAll(raw, []byte("[redacted]")))
}

// videoUpstreamError builds an error from a non-2xx upstream response. The HTTP
// status is the signal; any body is surfaced only as a short, secret-scrubbed
// reason (via videoSafeReason) — and NOT AT ALL on an auth-class status, whose
// body can echo the sent credential and tells the caller nothing actionable. A
// 429 is annotated as retryable so callers/clients can back off.
func videoUpstreamError(stage string, status int, raw []byte) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("%s failed (HTTP %d)", stage, status)
	}
	if status == http.StatusTooManyRequests {
		return fmt.Errorf("%s rate-limited (HTTP 429): %s; retry", stage, videoSafeReason(raw))
	}
	return fmt.Errorf("%s failed (HTTP %d): %s", stage, status, videoSafeReason(raw))
}
