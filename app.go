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

// Package ai mounts the Hanzo AI subsystem (LLM control plane, RAG,
// model hub, MCP management) into the unified cloud binary per HIP-0106.
//
// The legacy entry point at ~/work/hanzo/ai/main.go registers the
// existing the router ControllerRegister tree. Mount adapts that same
// ControllerRegister onto a zip.App via zip.AdaptNetHTTP so the routes
// continue to operate unchanged while running under the canonical
// zip-driven cloud entry.
//
// All ~309 X-Org-Id call-sites inside controllers/* continue to read
// gateway-minted identity headers (X-Org-Id, X-User-Id, X-User-Email)
// per HIP-0026 — the adapter does not strip headers; zip middleware in
// the cloud binary already mints them from the JWT before forwarding.
package ai

import (
	"net/http"
	"sync/atomic"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/fiber/v3/middleware/adaptor"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/routers"
)

// Mount registers AI's HTTP surface per HIP-0106 AND initializes the AI
// runtime so those routes actually serve.
//
// Routes under /v1/ai/* are forwarded to the registered handler (the
// the router ControllerRegister built by routers/router.go). The MountSpec
// contract (cloud.MountAll) gives each subsystem exactly one hook —
// Mount — and it owns BOTH route wiring and runtime initialization. So
// Mount calls Bootstrap(), the single shared boot sequence (DB, model
// config, balance/tier/rate-limit, filters, billing queue) that
// ends by publishing the handler via SetHandler. Without that call the
// adapter's getHandler() stays nil and every /v1/ai/* request 503s with
// "ai runtime not initialized" — the exact defect this fixes.
//
// The same Bootstrap() runs in the standalone cmd/aid entrypoint, so the
// runtime is defined ONCE and behaves identically embedded or standalone.
// Bootstrap is sync.Once-guarded, so calling it here is safe even if the
// process also calls it elsewhere.
// SECRETS ARRIVE AS AN INTERFACE, NOT AS THE HOST'S Deps. ai is a SUBSYSTEM: it
// is mounted by a host, and a subsystem that imports its host cannot be mounted
// by a second one — which is exactly what happened. hanzoai/cloud has two
// editions, and because this package took cloud.Deps, the private edition's
// cloud.Deps and the OSS edition's were different types with the same name and
// neither could mount ai.
//
// object.SetSecretStore already took ai's OWN interface (object.SecretStore:
// GetSecret/PutSecret), so cloud.Deps was carried across the boundary to reach
// one field and then discarded. Taking the interface directly costs the host one
// argument and removes the whole dependency: ai now imports zip and nothing of
// its host's.
func App(secrets object.SecretStore) (*zip.App, error) {
	// THE BODY CEILING IS A TRANSPORT SETTING, AND IT HAS TO BE SAID. Left unset,
	// fiber substitutes DefaultBodyLimit — 4 MiB — onto fasthttp's
	// MaxRequestBodySize, and the socket then refuses a 25 MiB transcription
	// upload before any handler runs. That is a 413 the audio endpoint never sees
	// and cannot explain, on a limit it believes is its own.
	//
	// So the ceiling is stated ABOVE the product limit, not equal to it, and that
	// gap is the point: an over-large upload reaches AudioTranscriptions, which
	// authenticates before it reports the size — the size of a body is not
	// something an unauthenticated caller gets to learn. Past the ceiling there is
	// nothing to be gained by reading further and the socket cuts it.
	app := zip.New(zip.Config{
		AppName:   "ai",
		BodyLimit: controllers.MaxTranscribeUpload + (1 << 20),
	})
	log := luxlog.Default()
	if log == nil {
		log = luxlog.New("module", "ai")
	}
	log.Info("ai: building routes", "prefix", "/v1/ai")

	// Bind the EMBEDDED KMS. cloud holds luxfi/kms in-process (apps/kms), so a
	// provider's "kms://NAME" resolves through a function call against the store
	// this binary already has open — no HTTP, no hostname, no standalone
	// deployment. deps.KMS was already being handed to us and thrown away, while
	// ai kept its own HTTP client pointed at kms.hanzo.ai; that client 404'd on
	// the path it used and 401'd on the correct one, so every kms:// reference
	// was resolving from an env var instead. See object/kms.go.
	object.SetSecretStore(secrets)

	// Route wiring is a pure, dependency-free concern (separately tested).
	routes(app)
	built.Store(app)

	// Runtime init (DB, providers, filters, billing) — the side that needs
	// real infrastructure. A failure here is fatal for the AI surface, so it
	// propagates as a precise mount error (cloud.MountAll wraps it as
	// "mount ai: ..."); it must not be swallowed nor panic the whole binary.

	// Three-mode fail-closed contract: with no DB configured (driverName
	// empty) the runtime cannot bootstrap. Mount the routes and let
	// handlerAdapter serve a 503 rather than hard-failing the unified binary.
	// A configured DB that then fails to init stays fatal (real misconfig).
	if conf.GetConfigString("driverName") == "" {
		log.Warn("ai: no DB configured (driverName empty); serving fail-closed (503)")
		return app, nil
	}
	log.Info("ai: initializing runtime")
	if err := Bootstrap(); err != nil {
		return nil, err
	}
	log.Info("ai: runtime initialized")

	// The Enso router flywheel (probe + trainer) is launched inside Bootstrap — the
	// ONE shared boot sequence both this Mount and cmd/aid run — so it boots identically
	// embedded or standalone (HIP-510). No launch here: it would double-start the
	// goroutines, since Bootstrap already did it above.
	return app, nil
}

// routes wires AI's HTTP surface onto the app. It has no infrastructure
// dependencies (no DB, no providers) and is what the route adapter tests
// exercise; App adds runtime init on top.
//
// AI owns the legacy /v1 route table, which lives at BARE /v1/* paths
// (/v1/chat/completions, /v1/chat, /v1/completions, /v1/models, /v1/messages,
// /v1/get-chats, … — ~200 routes in routers/router.go). That is exactly what
// the production api.hanzo.ai gateway forwards: every cloud-api backend uses an
// unchanged url_pattern (/v1/chat/completions → cloud-api:8000/v1/chat/completions),
// and even its /v1/ai/{path} endpoint rewrites to BARE /v1/{path}. Mounting only
// under a /v1/ai/* prefix would 404 every real request. So AI mounts the controller
// handler at /v1/* with no path rewrite.
//
// This is collision-safe BECAUSE AI registers LAST (priority 150, after kms=10 …
// commerce=100, plans=111, pricing=112, gateway=80). Fiber v3 gives an
// earlier-registered specific route (e.g. commerce's /v1/billing/balance,
// plans' /v1/plans/*) precedence over this later, broader /v1/* glob — verified
// empirically — so each owning subsystem still serves its own namespace and AI
// is the fallback for the rest of /v1/*. The composition root's
// /v1/<name>/health routes (registered before MountAll) likewise win over this
// glob, so liveness is unaffected.
// routes puts every address this service serves on the app, natively.
//
// It used to be a GLOB. A single app.All("/v1/*", relay) caught everything and
// forwarded it through a net/http adaptor into a second router that held the real
// table, and a short list of "promoted" addresses was registered specifically so
// the fleet document could at least NAME them — a glob tells a host a prefix and
// nothing under it, so three operations that were authenticated, reachable and
// answering in production had no generated SDK, no MCP tool and no CLI command,
// and nothing said who served them.
//
// Every address is its own route now, so every one is described, and the boundary
// the relay existed to cross is gone with it: no adaptor, no second router, no
// context to stow and restore across it.
func routes(app *zip.App) {
	routers.Register(app)
}

// built is the app this process serves. One value, published by App, so the
// standalone entrypoint and the ZAP transports reach the SAME one — with the same
// route table and the same filter chain — instead of each finding its own.
//
// It replaced a registered http.Handler set by a SetHandler the boot sequence
// called: the handler was a second router built at import time, and publishing it
// was the last step of a two-router arrangement that no longer exists.
var built atomic.Pointer[zip.App]

// Handler is the HTTP surface as an http.Handler, for the ZAP transports that carry
// an HTTP request over the binary protocol (controllers.InitZapHandlers and
// InitForwardBridge). It is the same app the socket serves, so a bridged request
// meets every filter a socketed one does.
//
// The router is resolved PER REQUEST, never captured. App.Fiber() hands out the
// current generation's router and a generation is a projection — the next build
// materialises a fresh one, and a captured pointer would keep serving the old
// table. zip panics on the mirror-image mistake for the same reason.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app := built.Load()
		if app == nil {
			http.Error(w, "ai runtime not initialized", http.StatusServiceUnavailable)
			return
		}
		adaptor.FiberApp(app.Fiber())(w, r)
	})
}
