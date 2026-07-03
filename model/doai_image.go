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
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/ai/proxy"
)

// DigitalOcean serves its diffusion (image) models — fal-hosted FLUX.1 and
// SDXL — through an ASYNCHRONOUS invoke API that is NOT the OpenAI
// /v1/images/generations shape. This file is the ONE client for that API. Both
// image callers drive it: the OpenAI-compatible /v1/images/generations handler
// (controllers/images_api.go) and the chat-stream image branch
// (model/openai.go). It presents a synchronous result (submit → poll →
// retrieve) so the callers can return an OpenAI-compatible response.
//
// Contract (base = provider ProviderUrl, e.g. https://inference.do-ai.run/v1):
//
//	submit    POST {base}/async-invoke
//	          {"model_id":"fal-ai/flux/schnell","input":{"prompt":"…", …}}
//	          → {"request_id":"…","status":"QUEUED"}
//	poll      GET  {base}/async-invoke/{request_id}/status
//	          → {"status":"COMPLETE"|"QUEUED"|"IN_PROGRESS"|"FAILED"|…}
//	retrieve  GET  {base}/async-invoke/{request_id}
//	          → {"images":[{"content_type":"image/jpeg","url":"https://…"}]}
//
// The Authorization is "Bearer {apiKey}" — the SAME do-ai key the chat routes
// resolve (never logged).

const (
	// doaiImagePollInterval is the delay between status polls. Image generation
	// is seconds, not instant; a short interval keeps latency low without
	// hammering the upstream.
	doaiImagePollInterval = 750 * time.Millisecond

	// doaiImageMaxWait bounds the total submit→complete wall time. FLUX schnell
	// returns in a few seconds; SDXL a bit longer. 120s is a generous ceiling
	// that still fails a wedged job rather than hanging the request forever.
	doaiImageMaxWait = 120 * time.Second
)

// GeneratedImage is one image from a generation call. Exactly one of URL or
// B64JSON is set, matching what the upstream returned (fal returns a URL).
type GeneratedImage struct {
	URL     string
	B64JSON string
}

// ImageGenResult is the structured result of an image generation: the images
// and the count actually produced (for billing via ModelResult.ImageCount).
type ImageGenResult struct {
	Images []GeneratedImage
}

// ImageGenRequest is the normalized, provider-agnostic image request the model
// layer accepts. Size/OutputFormat are optional; N defaults to 1.
type ImageGenRequest struct {
	// UpstreamModel is the do-ai/fal model_id (e.g. "fal-ai/flux/schnell").
	UpstreamModel string
	Prompt        string
	// N is the number of images (fal "num_images"); clamped to [1,10].
	N int
	// Size is an OpenAI-style "WxH" (e.g. "1024x1024"); passed through to fal
	// as image_size when set. Empty means the model default.
	Size string
	// OutputFormat is fal's format hint (e.g. "jpeg", "png"); optional.
	OutputFormat string
}

// GenerateImageDOAI runs a synchronous image generation against DigitalOcean's
// async fal image API: submit, poll to completion, retrieve. baseURL is the
// provider's ProviderUrl (…/v1); apiKey is the resolved do-ai key. It never
// logs the key. Returns the produced images or an error (a failed/timed-out job
// is an error, never a partial/silent success).
func GenerateImageDOAI(ctx context.Context, baseURL, apiKey string, req ImageGenRequest) (*ImageGenResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("image generation requires a non-empty prompt")
	}
	if req.UpstreamModel == "" {
		return nil, fmt.Errorf("image generation requires an upstream model id")
	}

	base := strings.TrimRight(baseURL, "/")
	client := proxy.ProxyHttpClient
	if client == nil {
		client = http.DefaultClient
	}

	requestID, err := doaiImageSubmit(ctx, client, base, apiKey, req)
	if err != nil {
		return nil, err
	}

	if err := doaiImageWait(ctx, client, base, apiKey, requestID); err != nil {
		return nil, err
	}

	return doaiImageRetrieve(ctx, client, base, apiKey, requestID)
}

// doaiImageInput is the fal "input" block. num_images is always sent; the rest
// are omitempty so a model that rejects an unknown field never sees it.
//
// ImageSize is fal's structured size — an OBJECT {"width":W,"height":H}, NOT a
// bare "WxH" string. fal tightened this: a string like "1024x1024" now fails
// upstream validation ("image_size: Input should be a valid dictionary or
// object"), so we serialize the parsed dimensions. Left as any + omitempty so a
// blank/unparseable size is dropped and the model default applies.
type doaiImageInput struct {
	Prompt              string `json:"prompt"`
	NumImages           int    `json:"num_images"`
	ImageSize           any    `json:"image_size,omitempty"`
	OutputFormat        string `json:"output_format,omitempty"`
	EnableSafetyChecker bool   `json:"enable_safety_checker"`
}

// doaiImageSizeObj is fal's {width,height} image_size object.
type doaiImageSizeObj struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// doaiImageSize converts an OpenAI-style "WxH" size (e.g. "1024x1024") into
// fal's structured image_size object. It returns nil (so image_size is omitted
// and the model default applies) when the size is empty or not a valid "WxH".
func doaiImageSize(size string) any {
	size = strings.TrimSpace(size)
	if size == "" {
		return nil
	}
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return nil
	}
	w, werr := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, herr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if werr != nil || herr != nil || w <= 0 || h <= 0 {
		return nil
	}
	return doaiImageSizeObj{Width: w, Height: h}
}

type doaiImageSubmitBody struct {
	ModelID string         `json:"model_id"`
	Input   doaiImageInput `json:"input"`
}

// doaiImageSubmit POSTs the async-invoke job and returns its request_id.
func doaiImageSubmit(ctx context.Context, client *http.Client, base, apiKey string, req ImageGenRequest) (string, error) {
	n := req.N
	if n < 1 {
		n = 1
	}
	if n > 10 {
		n = 10
	}

	body := doaiImageSubmitBody{
		ModelID: req.UpstreamModel,
		Input: doaiImageInput{
			Prompt:              req.Prompt,
			NumImages:           n,
			ImageSize:           doaiImageSize(req.Size),
			OutputFormat:        req.OutputFormat,
			EnableSafetyChecker: true,
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal image request: %w", err)
	}

	resp, err := doaiImageDo(ctx, client, http.MethodPost, base+"/async-invoke", apiKey, payload)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("image submit failed (HTTP %d): %s", resp.StatusCode, shortUpstreamErr(raw))
	}

	var out struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode image submit response: %w", err)
	}
	if out.RequestID == "" {
		return "", fmt.Errorf("image submit returned no request_id")
	}
	return out.RequestID, nil
}

// doaiImageWait polls the job status until it completes, fails, or the deadline
// (doaiImageMaxWait / ctx) elapses.
func doaiImageWait(ctx context.Context, client *http.Client, base, apiKey, requestID string) error {
	deadline := time.Now().Add(doaiImageMaxWait)
	statusURL := base + "/async-invoke/" + requestID + "/status"

	for {
		resp, err := doaiImageDo(ctx, client, http.MethodGet, statusURL, apiKey, nil)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("image status poll failed (HTTP %d): %s", resp.StatusCode, shortUpstreamErr(raw))
		}

		var st struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			return fmt.Errorf("decode image status: %w", err)
		}

		switch strings.ToUpper(st.Status) {
		case "COMPLETE", "COMPLETED", "SUCCEEDED", "SUCCESS":
			return nil
		case "FAILED", "ERROR", "CANCELED", "CANCELLED":
			return fmt.Errorf("image generation %s", strings.ToLower(st.Status))
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("image generation timed out after %s (last status: %s)", doaiImageMaxWait, st.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(doaiImagePollInterval):
		}
	}
}

// doaiImageRetrieve fetches the completed job result and extracts the images.
func doaiImageRetrieve(ctx context.Context, client *http.Client, base, apiKey, requestID string) (*ImageGenResult, error) {
	resp, err := doaiImageDo(ctx, client, http.MethodGet, base+"/async-invoke/"+requestID, apiKey, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image retrieve failed (HTTP %d): %s", resp.StatusCode, shortUpstreamErr(raw))
	}

	// fal image results expose images[]. Tolerate both a top-level "images" and
	// an "output"/"response"-wrapped one, since fal model families vary the
	// envelope; the image objects always carry url (+ optional content_type).
	var body struct {
		Images   []doaiImageObject `json:"images"`
		Output   *doaiImageEnv     `json:"output"`
		Response *doaiImageEnv     `json:"response"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode image result: %w", err)
	}

	images := body.Images
	if len(images) == 0 && body.Output != nil {
		images = body.Output.Images
	}
	if len(images) == 0 && body.Response != nil {
		images = body.Response.Images
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("image generation returned no images")
	}

	result := &ImageGenResult{Images: make([]GeneratedImage, 0, len(images))}
	for _, im := range images {
		if im.URL == "" && im.B64JSON == "" {
			continue
		}
		result.Images = append(result.Images, GeneratedImage{URL: im.URL, B64JSON: im.B64JSON})
	}
	if len(result.Images) == 0 {
		return nil, fmt.Errorf("image generation returned images with no url or data")
	}
	return result, nil
}

type doaiImageObject struct {
	URL     string `json:"url"`
	B64JSON string `json:"b64_json"`
}

type doaiImageEnv struct {
	Images []doaiImageObject `json:"images"`
}

// doaiImageDo builds and sends one authenticated request. The apiKey rides the
// Authorization header only (never logged, never in the URL or body).
func doaiImageDo(ctx context.Context, client *http.Client, method, url, apiKey string, body []byte) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("build image request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("image request to upstream failed: %w", err)
	}
	return resp, nil
}

// doaiErrBodyMax caps how much of a non-2xx upstream body is surfaced in an
// error. The HTTP status is the signal; the body is a short reason only, so fal
// internals are never echoed verbatim to the API caller.
const doaiErrBodyMax = 200

// shortUpstreamErr trims and length-caps an upstream error body for inclusion in
// an error message (adds an ellipsis when truncated).
func shortUpstreamErr(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > doaiErrBodyMax {
		return s[:doaiErrBodyMax] + "…"
	}
	return s
}
