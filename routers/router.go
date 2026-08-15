// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

// Package routers
// @APIVersion 1.70.0
// @Title Hanzo Cloud RESTful API
// @Description Swagger Docs of Hanzo Cloud Backend API
// @Contact cloud@hanzo.ai
// @SecurityDefinition AccessToken apiKey Authorization header
// @Schemes https,http
// @ExternalDocs Find out more about Hanzo Cloud
// @ExternalDocsUrl https://hanzo.ai/cloud
package routers

import (
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/web"
)

// App is the ai runtime's HTTP router: every /v1 route registers on it here,
// the filter chain is inserted at composition time, and the runtime serves it
// directly. Routes are explicit; there is no annotation-based registration.
var App = web.NewRouter()

func init() {
	initAPI()
}

func initAPI() {
	// Every CRUD resource — /v1/<subsystem>/<resource> — is generated from the
	// one table in resources.go. The routes written out below are the endpoints
	// that are NOT resources: the OpenAI-compatible surface, auth, and singletons.
	registerResources(App)

	// Provider-admin management surface (super-admin gated in authz_filter.go).
	// Reads/writes the SAME object.Provider records as the CRUD routes above.
	App.Router("/v1/admin/providers", &controllers.ApiController{}, "GET:GetAdminProviders")
	App.Router("/v1/admin/providers/toggle", &controllers.ApiController{}, "POST:ToggleAdminProvider")
	App.Router("/v1/admin/providers/primary", &controllers.ApiController{}, "POST:SetPrimaryAdminProvider")
	// Public, secret-free projection of /v1/models: who serves the listed catalog.
	// Registered beside the model routes below, not here — see /v1/models.

	// AI login-manager: org-scoped connections to third-party AI accounts. Curated
	// surface over object.Provider — authenticated org user (NOT super-admin);
	// keys are sealed into KMS, never returned. See controllers/connections_api.go.
	App.Router("/v1/ai/connections", &controllers.ApiController{}, "GET:GetAIConnections;POST:AddAIConnection")
	App.Router("/v1/ai/connections/:provider", &controllers.ApiController{}, "DELETE:DeleteAIConnection;POST:DeleteAIConnection")
	// OAuth "connect your provider login": authorize sends the org (from the
	// session) to the provider; callback seals the returned token into KMS exactly
	// like the BYOK path. See controllers/connections_oauth.go.
	App.Router("/v1/ai/connections/:provider/authorize", &controllers.ApiController{}, "GET:ConnectAIProvider")
	App.Router("/v1/ai/connections/:provider/callback", &controllers.ApiController{}, "GET:CallbackAIProvider")
	// Import a connected account's usage: unseal the org's key server-side, call the
	// provider's own usage/cost API, normalize to ProviderUsage. See
	// controllers/connections_usage.go.
	App.Router("/v1/ai/connections/:provider/usage", &controllers.ApiController{}, "GET:GetAIConnectionUsage")

	// Unified RAG ingest (upload | github | crawl | s3) → parse + chunk + embed into
	// {owner}-{store}-docs (Hanzo Vector + Search). Handler exists (docs_ingest.go);
	// this is its only route — the swagger @router annotation alone does not register it.
	App.Router("/v1/docs/ingest", &controllers.ApiController{}, "POST:IngestDocs")

	App.Router("/v1/generate-text-to-speech-audio", &controllers.ApiController{}, "POST:GenerateTextToSpeechAudio")
	App.Router("/v1/generate-text-to-speech-audio-stream", &controllers.ApiController{}, "GET:GenerateTextToSpeechAudioStream")
	App.Router("/v1/process-speech-to-text", &controllers.ApiController{}, "POST:ProcessSpeechToText")

	// Super-admin (authz_filter.go superAdminEndpoints): backfill the usage ledger
	// from DigitalOcean billing for windows native metering missed. Dry-run by default.
	App.Router("/v1/admin/usage/backfill-do", &controllers.ApiController{}, "POST:PostBackfillDOUsage")

	App.Router("/v1/install-patch", &controllers.ApiController{}, "POST:InstallPatch")

	App.Router("/v1/dev-bridge", &controllers.ApiController{}, "GET:DevBridge")

	App.Router("/v1/health", &controllers.ApiController{}, "GET:Health")
	App.Router("/v1/metrics", &controllers.ApiController{}, "GET:GetMetrics")

	// Unified chat — OpenAI-compatible completions with optional RAG.
	// /v1/chat is the new canonical route; /v1/chat/completions is kept as an
	// alias for OpenAI SDK compatibility.
	App.Router("/v1/chat", &controllers.ApiController{}, "POST:ChatCompletions")
	App.Router("/v1/chat/completions", &controllers.ApiController{}, "POST:ChatCompletions")
	App.Router("/v1/completions", &controllers.ApiController{}, "POST:ChatCompletions")
	// The public lane — a completion for a visitor with no account. It is a route of
	// its own rather than a mode of the one above, because the credentialed surface
	// must keep answering 401 to a caller whose key failed to load: served as a mode,
	// a missing Authorization header would silently become a free answer from a model
	// the caller did not ask for, and an SDK would report neither. Closed unless
	// PUBLIC_CHAT_DAILY is set.
	App.Router("/v1/chat/public", &controllers.ApiController{}, "POST:ChatCompletionsPublic")
	// OpenAI Responses API — the native wire protocol used by current Codex.
	// The controller adapts onto ChatCompletions so auth, routing, billing and
	// failover remain one policy path.
	App.Router("/v1/responses", &controllers.ApiController{}, "POST:Responses")
	App.Router("/v1/models", &controllers.ApiController{}, "GET:ListModels")
	// Who serves the catalog above — the same source, projected to a set of names.
	// Public and secret-free like the listing itself. A literal segment, so it
	// outscores no param route here (there is no /v1/models/:model) and cannot be
	// captured as a model id. Replaces the old top-level /v1/provider-flags, which
	// answered from the provider DB and drifted from what was actually served.
	App.Router("/v1/models/providers", &controllers.ApiController{}, "GET:GetModelProviders")
	// Access gating for limited-preview SKUs (enso): a caller requests/reads their own
	// standing; a SuperAdmin grants and lists. Registered as a deeper path than
	// /v1/models so the literal segment is not captured as a :param.
	App.Router("/v1/models/:model/access", &controllers.ApiController{}, "GET:GetModelAccessStatus;POST:RequestModelAccess")
	App.Router("/v1/admin/model-access", &controllers.ApiController{}, "GET:AdminListModelAccess;POST:AdminGrantModelAccess")
	App.Router("/v1/admin/reload-model-config", &controllers.ApiController{}, "POST:ReloadModelConfig")
	App.Router("/v1/admin/refresh-model-pricing", &controllers.ApiController{}, "POST:RefreshModelPricing")

	// OpenAI-compatible embeddings and Cohere/Jina-compatible rerank. Both ride
	// the same auth + provider routing as /v1/chat/completions.
	App.Router("/v1/embeddings", &controllers.ApiController{}, "POST:Embeddings")
	App.Router("/v1/rerank", &controllers.ApiController{}, "POST:Rerank")

	// OpenAI-compatible image generation. Same auth + provider routing; the
	// zen3-image family routes to do-ai's fal-hosted diffusion models.
	App.Router("/v1/images/generations", &controllers.ApiController{}, "POST:ImagesGenerations")

	// OpenAI Sora-style ASYNC text-to-video. Same auth + provider routing; the
	// zen3-video family (and wan2-2-t2v-a14b) route to the spark-video backend's
	// async /v1/videos API. Create returns a job id immediately; the client polls
	// {id} and downloads {id}/content — so the pod never holds a request open for
	// the minutes a generation takes (that ~104s hold was the console-proxy 502).
	// The static /generations route is registered BEFORE the /:id wildcard so a
	// POST to /generations can never be captured as an :id.
	App.Router("/v1/videos/generations", &controllers.ApiController{}, "POST:VideosGenerations")
	App.Router("/v1/videos/:id", &controllers.ApiController{}, "GET:RetrieveVideo")
	App.Router("/v1/videos/:id/content", &controllers.ApiController{}, "GET:VideoContent")

	// OpenAI-compatible text-to-speech (/v1/audio/speech) and speech-to-text
	// (/v1/audio/transcriptions). Same auth + model-route resolution as
	// chat/images/video → a BYO TTS/STT provider works transparently.
	// Completes native audio+image+video: /v1/audio/*, /v1/images/generations,
	// /v1/videos/generations all OpenAI-shaped on the one router.
	App.Router("/v1/audio/speech", &controllers.ApiController{}, "POST:AudioSpeech")
	App.Router("/v1/audio/transcriptions", &controllers.ApiController{}, "POST:AudioTranscriptions")
	// Zen-native generative audio verbs: voice (TTS), music, foley.
	App.Router("/v1/audio/voice", &controllers.ApiController{}, "POST:AudioMedia")
	App.Router("/v1/audio/music", &controllers.ApiController{}, "POST:AudioMedia")
	App.Router("/v1/audio/foley", &controllers.ApiController{}, "POST:AudioMedia")

	// The router-config surface — per-org settings (/v1/org/settings + /list), routing
	// defaults (/v1/router/defaults), the policy noun (GET|PUT /v1/router/policy), the
	// artifact-meta write (/v1/router/artifact-meta), and the ledger/rewards exports
	// (/v1/router/{ledger,rewards}) — is served ZAP-native, the ONE implementation
	// (controllers/zap_router-policy-stats.go + zap_verticals-and-misc.go). No controller
	// twin: RouterConfigBridge is only the HTTP transport binding — it dispatches
	// in-process through the SAME gateway registry, so there is one handler, no
	// split-brain (the twin drift is exactly what silently dropped customer data).
	// "*": the native handler is method-aware (GET/PUT policy, GET/PUT/DELETE settings)
	// and returns 405 for a verb it does not own. /list is a distinct path segment, so
	// it needs its own route (the matcher keys on exact segment count).
	App.Router("/v1/router/policy", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/router/defaults", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/router/ledger", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/router/rewards", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/router/artifact-meta", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/org/settings", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/org/settings/list", &controllers.ApiController{}, "*:RouterConfigBridge")

	// Per-request reward signal for the enso training loop: clients POST an outcome
	// keyed by the request_id they hold, scoped to their own org. /v1/feedback is the
	// signal-typed front door ({request_id, signal: up|down|regenerate|switch|…}) onto
	// the ONE reward join; the engine's online LinUCB observe is driven from here.
	App.Router("/v1/feedback", &controllers.ApiController{}, "POST:AddRoutingReward")

	// Self-scoped data ownership (org-admin, own org only): export or delete the
	// caller's OWN content-free routing ledger — the customer-facing right-to-
	// access + right-to-be-forgotten that pairs with the training opt-in.

	// Router observability aggregate (aggregates only, never raw events): the
	// admin savings-vs-perf panel reads the org-scoped form; world.hanzo.ai polls
	// ?scope=platform, a PUBLIC-safe aggregate with no $ levels or org identity.
	// Plus the per-org opt-in for contributing events to the shared base refresh
	// and the retrain job's published-state write.
	App.Router("/v1/router/stats", &controllers.ApiController{}, "GET:GetRouterStats")
	// Improvement time-series (reward + cost-saved + adoption over time, retrain
	// markers) — the world.hanzo.ai flywheel view. PUBLIC ?scope=platform, aggregates
	// only (task mix, never model ids). Balance+auth-exempt like /v1/router/stats.
	App.Router("/v1/router/history", &controllers.ApiController{}, "GET:GetRouterHistory")
	// Live Mean-Field Judge Panel state for the world.hanzo.ai dashboard. PUBLIC,
	// platform-global (model ids + scalars only, no org/user rows), balance+auth-exempt
	// like /v1/router/stats — the world widget polls it the same way.
	App.Router("/v1/router/judge-panel", &controllers.ApiController{}, "GET:GetRouterJudgePanel")

	// Live request-geo aggregate for the world.hanzo.ai Hanzo-mode globe. PUBLIC:
	// aggregates only (country/region counts + throughput rates), no auth, no IPs.
	// Balance-exempt via isBalanceExempt; the authz filter passes it through as a
	// non get-/update- controller name (same class as /v1/router/stats).
	App.Router("/v1/traffic/globe", &controllers.ApiController{}, "GET:GetTrafficGlobe")

	// Anthropic Messages API compatible endpoints
	App.Router("/v1/messages", &controllers.ApiController{}, "POST:AnthropicMessages")
	App.Router("/v1/messages/count_tokens", &controllers.ApiController{}, "POST:AnthropicCountTokens")

	App.Router("/v1/wecom-bot/callback/:botId", &controllers.ApiController{}, "GET:WecomBotVerifyUrl;POST:WecomBotHandleMessage")

	// Normalised document APIs (public).
	// Retrieval / RAG lives on /v1/chat itself — no separate chat-docs route.
	App.Router("/v1/search", &controllers.ApiController{}, "POST:SearchDocs")
	App.Router("/v1/index", &controllers.ApiController{}, "POST:IndexDocs")
	App.Router("/v1/search/stats", &controllers.ApiController{}, "GET:SearchDocsStats")
	App.Router("/v1/crawl", &controllers.ApiController{}, "POST:Crawl")

	// File-scoped RAG — the ONE canonical uploaded-file RAG surface (consolidates
	// the retired standalone chat-rag-api). Embed a file under a file_id, then
	// retrieve chunks scoped to that file (or a set of files) over the SAME
	// Search+Vector index as doc RAG.
	App.Router("/v1/rag/embed", &controllers.ApiController{}, "POST:RagEmbed")
	App.Router("/v1/rag/query", &controllers.ApiController{}, "POST:RagQuery")
	App.Router("/v1/rag/query-multiple", &controllers.ApiController{}, "POST:RagQueryMultiple")
	App.Router("/v1/rag/delete", &controllers.ApiController{}, "POST:RagDelete")
	App.Router("/v1/rag/context", &controllers.ApiController{}, "GET:RagContext")

	// LibreChat-compat RAG — the FIXED contract hanzo.chat's RAG client calls at
	// RAG_API_URL. Pointing RAG_API_URL=https://api.hanzo.ai/v1 retires the
	// standalone chat-rag-api with no chat-repo change. Thin projection over the
	// same object.Rag* logic as /v1/rag/*.
	App.Router("/v1/embed", &controllers.ApiController{}, "POST:RagEmbedMultipart")
	App.Router("/v1/query", &controllers.ApiController{}, "POST:RagQueryCompat")
	App.Router("/v1/query_multiple", &controllers.ApiController{}, "POST:RagQueryMultipleCompat")
	App.Router("/v1/documents", &controllers.ApiController{}, "DELETE:RagDeleteDocuments")
	App.Router("/v1/documents/:file_id/context", &controllers.ApiController{}, "GET:RagDocumentContext")

	// Memory subsystem — cloud backend of the unified memory interface.
	// Per-user scoped; identity comes from gateway IAM headers, never the body.
	App.Router("/v1/memory/remember", &controllers.ApiController{}, "POST:MemoryRemember")
	App.Router("/v1/memory/search", &controllers.ApiController{}, "GET:MemorySearch")
	App.Router("/v1/memory/list", &controllers.ApiController{}, "GET:MemoryList")
	App.Router("/v1/memory/recall", &controllers.ApiController{}, "GET:MemoryRecall")
	App.Router("/v1/memory/facts", &controllers.ApiController{}, "GET:MemoryFacts")
	App.Router("/v1/memory/update", &controllers.ApiController{}, "POST:MemoryUpdate")
	App.Router("/v1/memory/delete", &controllers.ApiController{}, "POST:MemoryDelete")
}
