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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/hanzoai/ai/proxy"
)

// The upstream text-to-video model (the zen3-video family → wan2-2-t2v-a14b on
// the spark GB10 backend) is served through the OpenAI Sora-style ASYNCHRONOUS
// /v1/videos API. This file is the ONE client for that API. It exposes the three
// async primitives — CREATE, RETRIEVE (status), and DOWNLOAD (content) — so the
// /v1/videos/generations handler can present the SAME async lifecycle to its own
// callers (create → poll → download) WITHOUT holding a request open for the
// minutes a generation takes. Holding it open was the ~104s block that timed out
// the console /ai proxy and returned a 502 while the generation actually
// succeeded; splitting the lifecycle across three fast requests removes it.
//
// Contract (base = provider ProviderUrl, e.g. https://spark-video.hanzo.ai/v1):
//
//	create    POST {base}/videos
//	          {"model":"wan2-2-t2v-a14b","prompt":"…"}
//	          → 200/202 {"id":"video_…","object":"video","status":"queued"}
//	retrieve  GET  {base}/videos/{id}
//	          → {"id","model","object":"video",
//	             "status":"queued"|"in_progress"|"completed"|"failed",
//	             "error":null|{…}}
//	download  GET  {base}/videos/{id}/content
//	          → Content-Type video/mp4, the raw MP4 bytes (valid ONLY once the job
//	            is "completed"; while "in_progress" this returns a tiny JSON
//	            placeholder, which the download primitive rejects rather than pass
//	            off as video media).
//
// The Authorization is "Bearer {apiKey}" — the SAME provider key the chat/image
// routes resolve (never logged). The completed video bytes live ONLY behind the
// key-authenticated /content endpoint, so the handler proxies them server-side
// and the provider key never leaves the server.

// doaiVideoContentMax caps how many bytes the download primitive reads from
// /content. Observed real wan2-2-t2v clips are ~0.4 MB; 16 MiB is a hard safety
// ceiling (~40× the observed size — ample headroom for a longer/higher-res
// legitimate clip) that still stops a runaway/oversized response from exhausting
// memory. It is set deliberately low because this pod is single-replica and also
// serves chat/image/RAG; the handler additionally guards the download path with a
// concurrency semaphore so the aggregate is bounded too.
const doaiVideoContentMax = 16 << 20

// VideoGenRequest is the normalized, provider-agnostic create request the model
// layer accepts. The async /videos API produces exactly one video per create.
type VideoGenRequest struct {
	// UpstreamModel is the upstream model id (e.g. "wan2-2-t2v-a14b").
	UpstreamModel string
	Prompt        string
	// Size is an optional OpenAI-style "WxH" hint passed through when set.
	Size string
	// Seconds is an optional clip-length hint (upstream default when 0).
	Seconds int
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

// doaiVideoRecord is the OpenAI video object returned by create and retrieve.
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

// CreateVideoDOAI POSTs the create request and returns the upstream job id and
// its initial status ("queued"/"in_progress"). It does NOT wait for completion —
// that is the caller's job to poll via RetrieveVideoDOAI. baseURL is the
// provider's ProviderUrl (…/v1); apiKey is the resolved provider key (never
// logged).
func CreateVideoDOAI(ctx context.Context, baseURL, apiKey string, req VideoGenRequest) (id, status string, err error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return "", "", fmt.Errorf("video generation requires a non-empty prompt")
	}
	if req.UpstreamModel == "" {
		return "", "", fmt.Errorf("video generation requires an upstream model id")
	}

	body := doaiVideoCreateBody{
		Model:   req.UpstreamModel,
		Prompt:  req.Prompt,
		Size:    req.Size,
		Seconds: req.Seconds,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", "", fmt.Errorf("marshal video request: %w", err)
	}

	base := strings.TrimRight(baseURL, "/")
	resp, err := doaiVideoDo(ctx, videoHTTPClient(), http.MethodPost, base+"/videos", apiKey, payload)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, doaiErrBodyMax*8))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", videoUpstreamError("video create", resp.StatusCode, raw)
	}

	var rec doaiVideoRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return "", "", fmt.Errorf("decode video create response: %w", err)
	}
	if rec.ID == "" {
		return "", "", fmt.Errorf("video create returned no id")
	}
	if rec.Status == "" {
		rec.Status = "queued"
	}
	return rec.ID, rec.Status, nil
}

// RetrieveVideoDOAI performs ONE status poll of an upstream job and returns its
// current status plus, when the job failed, a short secret-scrubbed reason. It
// does NOT loop — the handler drives polling from its own client, one HTTP round
// trip per /v1/videos/{id} request, so the pod never holds a request open across
// the minutes a generation takes.
func RetrieveVideoDOAI(ctx context.Context, baseURL, apiKey, id string) (status, errMsg string, err error) {
	if strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("video retrieve requires an id")
	}
	base := strings.TrimRight(baseURL, "/")
	resp, err := doaiVideoDo(ctx, videoHTTPClient(), http.MethodGet, base+"/videos/"+id, apiKey, nil)
	if err != nil {
		return "", "", err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, doaiErrBodyMax*8))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", videoUpstreamError("video status poll", resp.StatusCode, raw)
	}

	var rec doaiVideoRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return "", "", fmt.Errorf("decode video status: %w", err)
	}
	if rec.Error != nil && rec.Error.Message != "" {
		errMsg = videoSafeReason([]byte(rec.Error.Message))
	}
	return rec.Status, errMsg, nil
}

// DownloadVideoBytesDOAI fetches the completed video bytes from /content and
// returns them raw (NOT base64) with their media type, so the handler can stream
// them to the client inline. The bytes live ONLY behind this key-authenticated
// endpoint, so the handler proxies them server-side and never exposes the
// provider URL/key. A non-video content type (notably the application/json
// placeholder the async API returns while the job is still in_progress) is
// rejected rather than handed back as a clip.
func DownloadVideoBytesDOAI(ctx context.Context, baseURL, apiKey, id string) (data []byte, mime string, err error) {
	if strings.TrimSpace(id) == "" {
		return nil, "", fmt.Errorf("video download requires an id")
	}
	base := strings.TrimRight(baseURL, "/")
	resp, err := doaiVideoDo(ctx, videoHTTPClient(), http.MethodGet, base+"/videos/"+id+"/content", apiKey, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, doaiErrBodyMax+1))
		return nil, "", videoUpstreamError("video download", resp.StatusCode, raw)
	}

	// A completed job must return video media. Accept ONLY a video/* content type,
	// a generic binary stream (application/octet-stream), or a missing header (some
	// CDNs omit it on binary downloads) — an allowlist, not allow-by-default. Any
	// other EXPLICIT type — notably the application/json placeholder the async API
	// returns while the job is still in_progress — means the payload is NOT a video.
	mime = strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if i := strings.IndexByte(mime, ';'); i >= 0 { // drop any "; charset=…" parameter
		mime = strings.TrimSpace(mime[:i])
	}
	if mime != "" && mime != "application/octet-stream" && !strings.HasPrefix(mime, "video/") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, doaiErrBodyMax+1))
		return nil, "", fmt.Errorf("video content not ready (upstream returned %q, not video media): %s", mime, videoSafeReason(raw))
	}
	// Normalize the surfaced media type: a bare stream / missing header defaults to
	// video/mp4 (what the backend serves); an explicit video/* type is kept as-is.
	if mime == "" || mime == "application/octet-stream" {
		mime = "video/mp4"
	}

	data, err = io.ReadAll(io.LimitReader(resp.Body, doaiVideoContentMax+1))
	if err != nil {
		return nil, "", fmt.Errorf("read video content: %w", err)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("video generation returned empty content")
	}
	if int64(len(data)) > doaiVideoContentMax {
		return nil, "", fmt.Errorf("video content exceeds %d bytes", doaiVideoContentMax)
	}
	return data, mime, nil
}

// videoHTTPClient returns the shared proxy HTTP client (falling back to the
// default), so upstream video calls ride the same egress path as every other
// provider request.
func videoHTTPClient() *http.Client {
	if proxy.ProxyHttpClient != nil {
		return proxy.ProxyHttpClient
	}
	return http.DefaultClient
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
