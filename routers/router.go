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
	"github.com/zap-proto/zip"
)

// Register binds every /v1 route this service serves onto the app, and installs
// the filter chain around them. Routes are explicit; there is no annotation-based
// registration.
//
// ONE entry point, called by ai.App. It used to be an init() writing onto a
// package-level router, so the table existed the moment the package was imported —
// before anything could say what it should be registered ON, and with no way for a
// test to ask for a clean one.
//
// THE FILTERS GO ON FIRST, and that is not cosmetic: the router walks its stack in
// registration order, so a filter added after a route never runs before it. The
// chain would still be installed, still be tested, and simply not be in front of
// anything.
func Register(app *zip.App) {
	InstallFilters(app)
	registerResources(app)
	registerAPI(app)
	registerFinetune(app)
}

func registerAPI(app *zip.App) {
	// Every CRUD resource — /v1/<subsystem>/<resource> — is generated from the one
	// table in resources.go, by Register above. What follows is the endpoints that
	// are NOT resources: the OpenAI-compatible surface, auth, and singletons.

	// Provider-admin management surface (super-admin gated in authz_filter.go).
	// Reads/writes the SAME object.Provider records as the CRUD routes above.
	route(app, "/v1/admin/providers", "GET:GetAdminProviders")
	route(app, "/v1/admin/providers/toggle", "POST:ToggleAdminProvider")
	route(app, "/v1/admin/providers/primary", "POST:SetPrimaryAdminProvider")
	// Public, secret-free projection of /v1/models: who serves the listed catalog.
	// Registered beside the model routes below, not here — see /v1/models.

	// AI login-manager: org-scoped connections to third-party AI accounts. Curated
	// surface over object.Provider — authenticated org user (NOT super-admin);
	// keys are sealed into KMS, never returned. See controllers/connections_api.go.
	route(app, "/v1/ai/connections", "GET:GetAIConnections;POST:AddAIConnection")
	route(app, "/v1/ai/connections/:provider", "DELETE:DeleteAIConnection;POST:DeleteAIConnection")
	// OAuth "connect your provider login": authorize sends the org (from the
	// session) to the provider; callback seals the returned token into KMS exactly
	// like the BYOK path. See controllers/connections_oauth.go.
	route(app, "/v1/ai/connections/:provider/authorize", "GET:ConnectAIProvider")
	route(app, "/v1/ai/connections/:provider/callback", "GET:CallbackAIProvider")
	// Import a connected account's usage: unseal the org's key server-side, call the
	// provider's own usage/cost API, normalize to ProviderUsage. See
	// controllers/connections_usage.go.
	route(app, "/v1/ai/connections/:provider/usage", "GET:GetAIConnectionUsage")

	// Super-admin (authz_filter.go superAdminEndpoints): backfill the usage ledger
	// from DigitalOcean billing for windows native metering missed. Dry-run by default.
	route(app, "/v1/admin/usage/backfill-do", "POST:PostBackfillDOUsage")

	// No /v1/health here: the HOST owns it (cloud/serve.go), answering for
	// every mounted plane with degradation detail. A second declaration in
	// this table panics the mount — it took the AI instance down at
	// v1.801.565, discovered only when the first lazy /v1 call arrived.
	route(app, "/v1/metrics", "GET:GetMetrics")

	// Unified chat — OpenAI-compatible completions with optional RAG.
	// /v1/chat is the new canonical route; /v1/chat/completions is kept as an
	// alias for OpenAI SDK compatibility.
	route(app, "/v1/chat", "POST:ChatCompletions")
	route(app, "/v1/chat/completions", "POST:ChatCompletions")
	route(app, "/v1/completions", "POST:ChatCompletions")
	// The public lane — a completion for a visitor with no account. It is a route of
	// its own rather than a mode of the one above, because the credentialed surface
	// must keep answering 401 to a caller whose key failed to load: served as a mode,
	// a missing Authorization header would silently become a free answer from a model
	// the caller did not ask for, and an SDK would report neither. Closed unless
	// PUBLIC_CHAT_DAILY is set.
	route(app, "/v1/chat/public", "POST:ChatCompletionsPublic")
	// OpenAI Responses API — the native wire protocol used by current Codex.
	// The controller adapts onto ChatCompletions so auth, routing, billing and
	// failover remain one policy path.
	route(app, "/v1/responses", "POST:Responses")
	route(app, "/v1/models", "GET:ListModels")
	// Who serves the catalog above — the same source, projected to a set of names.
	// Public and secret-free like the listing itself. A literal segment, so it
	// outscores no param route here (there is no /v1/models/:model) and cannot be
	// captured as a model id. Replaces the old top-level /v1/provider-flags, which
	// answered from the provider DB and drifted from what was actually served.
	route(app, "/v1/models/providers", "GET:GetModelProviders")
	// Access gating for limited-preview SKUs (enso): a caller requests/reads their own
	// standing; a SuperAdmin grants and lists. Registered as a deeper path than
	// /v1/models so the literal segment is not captured as a :param.
	route(app, "/v1/models/:model/access", "GET:GetModelAccessStatus;POST:RequestModelAccess")
	route(app, "/v1/admin/model-access", "GET:AdminListModelAccess;POST:AdminGrantModelAccess")
	route(app, "/v1/admin/reload-model-config", "POST:ReloadModelConfig")
	route(app, "/v1/admin/refresh-model-pricing", "POST:RefreshModelPricing")

	// OpenAI-compatible embeddings and Cohere/Jina-compatible rerank. Both ride
	// the same auth + provider routing as /v1/chat/completions.
	route(app, "/v1/embeddings", "POST:Embeddings")
	route(app, "/v1/rerank", "POST:Rerank")

	// OpenAI-compatible image generation. Same auth + provider routing; the
	// zen3-image family routes to do-ai's fal-hosted diffusion models.
	route(app, "/v1/images/generations", "POST:ImagesGenerations")

	// OpenAI Sora-style ASYNC text-to-video. Same auth + provider routing; the
	// zen3-video family (and wan2-2-t2v-a14b) route to the spark-video backend's
	// async /v1/videos API. Create returns a job id immediately; the client polls
	// {id} and downloads {id}/content — so the pod never holds a request open for
	// the minutes a generation takes (that ~104s hold was the console-proxy 502).
	// The static /generations route is registered BEFORE the /:id wildcard so a
	// POST to /generations can never be captured as an :id.
	route(app, "/v1/videos/generations", "POST:VideosGenerations")
	route(app, "/v1/videos/:id", "GET:RetrieveVideo")
	route(app, "/v1/videos/:id/content", "GET:VideoContent")

	// OpenAI-compatible text-to-speech (/v1/audio/speech) and speech-to-text
	// (/v1/audio/transcriptions). Same auth + model-route resolution as
	// chat/images/video → a BYO TTS/STT provider works transparently.
	// Completes native audio+image+video: /v1/audio/*, /v1/images/generations,
	// /v1/videos/generations all OpenAI-shaped on the one router.
	route(app, "/v1/audio/speech", "POST:AudioSpeech")
	route(app, "/v1/audio/transcriptions", "POST:AudioTranscriptions")
	// Zen-native generative audio verbs: voice (TTS), music, foley.
	route(app, "/v1/audio/voice", "POST:AudioMedia")
	route(app, "/v1/audio/music", "POST:AudioMedia")
	route(app, "/v1/audio/foley", "POST:AudioMedia")

	// The same audio with the turns joined up: a spoken conversation over one
	// socket, speaking the OpenAI realtime wire so clients written against it
	// work unchanged. hanzoai/voice holds the conversation, hanzoai/speech holds
	// the models, and this is the host — /v1/voice answered 404 in production
	// while that service sat finished for want of anywhere to run.
	//
	// Adapted rather than routed to a controller: a WebSocket has no
	// request/response pair to map a method onto, and its writer has to survive
	// as far as Hijack. Two calls because a browser cannot put a header on a
	// WebSocket — the bearer is spent on the POST, and what goes in the URL is a
	// one-use ticket that carries no claims.
	//
	// It stays adapted until hanzoai/voice publishes handlers rather than a
	// router, which is an upstream change and not one this repo can make: Routes
	// (serve.go:108) is the ONLY exported method on *Voice, and the three it
	// registers — session, talk, health (serve.go:116, 141, 184) — are
	// unexported, so there is nothing here to bind a native route to. The socket
	// is the second half: coder/websocket's Accept refuses a writer that is not
	// an http.Hijacker (accept.go:130).
	//
	// So h is an *http.ServeMux, and these three paths are matched twice, by two
	// routers that disagree about paths. voice_mux_test.go measures where:
	// GET /v1/voice/ matches this /v1/voice route, reaches the mux, which has no
	// pattern for it and answers out of net/http — "404 page not found",
	// text/plain, nosniff — the one 404 on this surface that is not RFC 9457.
	//
	// Nil when no IAM is configured, and then none of this is served: a
	// WebSocket does not observe the same-origin policy, so an ungated one is
	// any page on the internet opening a microphone as whoever is signed in.
	if h := controllers.VoiceHandler(); h != nil {
		talk := zip.AdaptNetHTTP(h)
		app.Post("/v1/voice/session", talk)
		app.Get("/v1/voice", talk)
		app.Get("/v1/voice/health", talk)
	}

	// The router-config surface — per-org settings (/v1/ai/org/settings + /list), routing
	// defaults (/v1/ai/router/defaults), the policy noun (GET|PUT /v1/ai/router/policy), the
	// artifact-meta write (/v1/ai/router/artifact-meta), and the ledger/rewards exports
	// (/v1/ai/router/{ledger,rewards}) — is served ZAP-native, the ONE implementation
	// (controllers/zap_router-policy-stats.go + zap_verticals-and-misc.go). No controller
	// twin: RouterConfigBridge is only the HTTP transport binding — it dispatches
	// in-process through the SAME gateway registry, so there is one handler, no
	// split-brain (the twin drift is exactly what silently dropped customer data).
	// "*": the native handler is method-aware (GET/PUT policy, GET/PUT/DELETE settings)
	// and returns 405 for a verb it does not own. /list is a distinct path segment, so
	// it needs its own route (the matcher keys on exact segment count).
	route(app, "/v1/ai/router/policy", "*:RouterConfigBridge")
	route(app, "/v1/ai/router/defaults", "*:RouterConfigBridge")
	route(app, "/v1/ai/router/ledger", "*:RouterConfigBridge")
	route(app, "/v1/ai/router/rewards", "*:RouterConfigBridge")
	route(app, "/v1/ai/router/artifact-meta", "*:RouterConfigBridge")
	route(app, "/v1/ai/org/settings", "*:RouterConfigBridge")
	route(app, "/v1/ai/org/settings/list", "*:RouterConfigBridge")

	// Per-request reward signal for the enso training loop: clients POST an outcome
	// keyed by the request_id they hold, scoped to their own org. /v1/ai/feedback is
	// the signal-typed entry point ({request_id, signal: up|down|regenerate|switch|…})
	// onto the ONE reward join; the engine's online LinUCB observe is driven from here.
	route(app, "/v1/ai/feedback", "POST:AddRoutingReward")

	// Self-scoped data ownership (org-admin, own org only): export or delete the
	// caller's OWN content-free routing ledger — the customer-facing right-to-
	// access + right-to-be-forgotten that pairs with the training opt-in.

	// Router observability aggregate (aggregates only, never raw events): the
	// admin savings-vs-perf panel reads the org-scoped form; world.hanzo.ai polls
	// ?scope=platform, a PUBLIC-safe aggregate with no $ levels or org identity.
	// Plus the per-org opt-in for contributing events to the shared base refresh
	// and the retrain job's published-state write.
	route(app, "/v1/ai/router/stats", "GET:GetRouterStats")
	// Improvement time-series (reward + cost-saved + adoption over time, retrain
	// markers) — the world.hanzo.ai flywheel view. PUBLIC ?scope=platform, aggregates
	// only (task mix, never model ids). Balance+auth-exempt like /v1/ai/router/stats.
	route(app, "/v1/ai/router/history", "GET:GetRouterHistory")
	// Live Mean-Field Judge Panel state for the world.hanzo.ai dashboard. PUBLIC,
	// platform-global (model ids + scalars only, no org/user rows), balance+auth-exempt
	// like /v1/ai/router/stats — the world widget polls it the same way.
	route(app, "/v1/ai/router/judge-panel", "GET:GetRouterJudgePanel")

	// Live request-geo aggregate for the world.hanzo.ai Hanzo-mode globe. PUBLIC:
	// aggregates only (country/region counts + throughput rates), no auth, no IPs.
	// Balance-exempt via isBalanceExempt; the authz filter passes it through as a
	// non get-/update- controller name (same class as /v1/ai/router/stats).
	route(app, "/v1/ai/traffic/globe", "GET:GetTrafficGlobe")

	// Anthropic Messages API compatible endpoints
	route(app, "/v1/messages", "POST:AnthropicMessages")
	route(app, "/v1/messages/count_tokens", "POST:AnthropicCountTokens")

	// Normalised document APIs (public).
	// Retrieval / RAG lives on /v1/chat itself — no separate chat-docs route.
	route(app, "/v1/search", "POST:SearchDocs")
	route(app, "/v1/index", "POST:IndexDocs")
	route(app, "/v1/search/stats", "GET:SearchDocsStats")
	// NO /v1/crawl ROUTE HERE. ai does not fetch the web itself — object.SetFetcher
	// says so, and nothing in this module ever calls it — so this address reached a
	// capability only a host has. Standalone it could answer nothing; mounted,
	// the host serves the same address with its own typed op, and one address claimed
	// by two apps is something a fleet cannot route. The crawl ai DOES offer is the
	// one its host feeds, over ZAP (zap_rag-search-crawl.go).

	// RAG — the ONE retrieval surface, one index. Ingest writes it (upload | github
	// | crawl | s3 → parse + chunk + embed into {owner}-{store}-docs, Hanzo Vector +
	// Search); embed writes one file under a file_id; query/query-multiple/context
	// read it back, scoped to a file or a set of them; delete removes one.
	// Ingest and embed are two ways IN to the same index, so they answer under the
	// noun that owns it. Ingest used to answer at a root of its own, which made
	// docs, documents and rag three addresses over one store.
	route(app, "/v1/ai/rag/ingest", "POST:IngestDocs")
	route(app, "/v1/ai/rag/embed", "POST:RagEmbed")
	route(app, "/v1/ai/rag/query", "POST:RagQuery")
	route(app, "/v1/ai/rag/query-multiple", "POST:RagQueryMultiple")
	route(app, "/v1/ai/rag/delete", "POST:RagDelete")
	route(app, "/v1/ai/rag/context", "GET:RagContext")

	// Memory subsystem — cloud backend of the unified memory interface.
	// Per-user scoped; identity comes from gateway IAM headers, never the body.
	route(app, "/v1/ai/memory/remember", "POST:MemoryRemember")
	route(app, "/v1/ai/memory/search", "GET:MemorySearch")
	route(app, "/v1/ai/memory/list", "GET:MemoryList")
	route(app, "/v1/ai/memory/recall", "GET:MemoryRecall")
	route(app, "/v1/ai/memory/facts", "GET:MemoryFacts")
	route(app, "/v1/ai/memory/update", "POST:MemoryUpdate")
	route(app, "/v1/ai/memory/delete", "POST:MemoryDelete")
}
