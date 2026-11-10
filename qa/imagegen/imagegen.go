// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package imagegen is the QA suite for #131, Image Generation Consistency: a
// reproducible test battery for the gateway's image surface — POST
// /v1/images/generations across the fal-hosted diffusion family (zen3-image* →
// fal-ai/flux/schnell, zen3-image-sdxl → fal-ai/fast-sdxl).
//
// The offline half is what CI runs: metadata.go proves the image validator —
// magic-byte MIME sniffing, dimension decode, non-empty payload — against
// fixtures.go containers built byte-for-byte at run time (PNG/JPEG/GIF/WEBP at
// exact dimensions) and their corrupt twins (truncated streams, a PNG body
// wearing a JPEG label, an HTML error page where pixels were promised).
// params.go proves the contract checkers against canned conforming AND
// divergent bodies: fal's structured image_size object regressing to a bare
// "WxH" string, a silently dropped size, a mutated prompt, an unclamped
// num_images.
//
// The live half talks to the real gateway and skips without opt-in
// (HANZO_QA_LIVE=1 + HANZO_API_KEY): the resolution/aspect-ratio matrix
// downloads every returned artifact and demands its decoded dimensions match
// the request, the prompt-adherence corpus pins complex compositions, the
// sequential lanes probe character consistency, the typography lanes probe
// embedded text, and a dedicated lane records that response_format is parsed
// but silently ignored. HANZO_QA_REPORT writes every live verdict to a JSON
// artifact plus a Markdown twin beside it — the request/response trace the
// acceptance criteria ask for. See README.md for the lane table and findings.
package imagegen

import (
	"os"
	"strings"
	"time"
)

// DefaultBase is the production gateway, HANZO_API_URL may name it with or
// without /v1. These env names are shared with qa/audio (#133),
// qa/crossmodel (#138) and qa/multimodal (#135) on purpose — one CI secret
// configures every suite.
const DefaultBase = "https://api.hanzo.ai"

const (
	BaseURLEnv  = "HANZO_API_URL"
	KeyEnv      = "HANZO_API_KEY"   // keyed lanes; absent → they skip
	ReportEnv   = "HANZO_QA_REPORT" // artifact path for report.json (+ a .md twin)
	LiveGateEnv = "HANZO_QA_LIVE"   // opts the live lanes into real network calls

	// GenerationsPath is the only image endpoint the relay exposes — there is
	// no /images/edits door (a documented #131 finding).
	GenerationsPath = "/images/generations"
	EditsPath       = "/images/edits"

	// RequestTimeout bounds one generation round trip: the relay's own upstream
	// budget is 130 s (controllers/images_api.go), plus artifact download.
	RequestTimeout = 150 * time.Second
)

// Pinned SKUs under test, from the routing table and imagePricePerImageCents
// (controllers/model_pricing.go). Catalog drift must fail loudly instead of
// silently testing a different engine.
const (
	ZenImage     = "zen3-image"      // → fal-ai/flux/schnell   ($0.05/image)
	ZenImageSDXL = "zen3-image-sdxl" // → fal-ai/fast-sdxl     ($0.06/image)
)

// BaseURL resolves the gateway root, ending in /v1 (the shape every qa suite prints).
func BaseURL() string {
	base := strings.TrimRight(os.Getenv(BaseURLEnv), "/")
	if base == "" {
		base = DefaultBase
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base
}

// LiveOpted reports whether the live lanes may touch the network at all.
func LiveOpted() bool { return os.Getenv(LiveGateEnv) == "1" }

// Keyed reports whether a gateway credential is present.
func Keyed() bool { return os.Getenv(KeyEnv) != "" }
