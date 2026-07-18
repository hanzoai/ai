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
// existing beego ControllerRegister tree. Mount adapts that same
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
	"sync"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/conf"
)

// Mount registers AI's HTTP surface per HIP-0106 AND initializes the AI
// runtime so those routes actually serve.
//
// Routes under /v1/ai/* are forwarded to the registered handler (the
// beego ControllerRegister built by routers/router.go). The MountSpec
// contract (cloud.MountAll) gives each subsystem exactly one hook —
// Mount — and it owns BOTH route wiring and runtime initialization. So
// Mount calls Bootstrap(), the single shared boot sequence (DB, model
// config, balance/tier/rate-limit, beego filters, billing queue) that
// ends by publishing the handler via SetHandler. Without that call the
// adapter's getHandler() stays nil and every /v1/ai/* request 503s with
// "ai runtime not initialized" — the exact defect this fixes.
//
// The same Bootstrap() runs in the standalone cmd/aid entrypoint, so the
// runtime is defined ONCE and behaves identically embedded or standalone.
// Bootstrap is sync.Once-guarded, so calling it here is safe even if the
// process also calls it elsewhere.
func Mount(app *zip.App, deps cloud.Deps) error {
	log := deps.Logger
	if log == nil {
		log = luxlog.New("module", "ai")
	}
	log.Info("ai: mounting routes", "prefix", "/v1/ai")

	// Route wiring is a pure, dependency-free concern (separately tested).
	mountRoutes(app)

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
		return nil
	}
	log.Info("ai: initializing runtime")
	if err := Bootstrap(); err != nil {
		return err
	}
	log.Info("ai: runtime initialized")

	// The Enso router flywheel (probe + trainer) is launched inside Bootstrap — the
	// ONE shared boot sequence both this Mount and cmd/aid run — so it boots identically
	// embedded or standalone (HIP-510). No launch here: it would double-start the
	// goroutines, since Bootstrap already did it above.
	return nil
}

// mountRoutes wires AI's HTTP surface onto the zip app. It has no
// infrastructure dependencies (no DB, no providers) and is what the route
// adapter tests exercise; Mount adds runtime init on top.
//
// AI owns the legacy /v1 route table, which lives at BARE /v1/* paths
// (/v1/chat/completions, /v1/chat, /v1/completions, /v1/models, /v1/messages,
// /v1/get-chats, … — ~200 routes in routers/router.go). That is exactly what
// the production api.hanzo.ai gateway forwards: every cloud-api backend uses an
// unchanged url_pattern (/v1/chat/completions → cloud-api:8000/v1/chat/completions),
// and even its /v1/ai/{path} endpoint rewrites to BARE /v1/{path}. Mounting only
// under a /v1/ai/* prefix would 404 every real request. So AI mounts the beego
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
func mountRoutes(app *zip.App) {
	app.All("/v1/*", zip.AdaptNetHTTP(handlerAdapter{}))
}

// handlerAdapter forwards each request under /v1/* to the registered runtime
// handler (the beego ControllerRegister) or returns 503 if none. The path is
// passed through unchanged: beego's routes are registered at the same bare /v1/*
// paths the gateway forwards, so no rewrite is needed (and none must happen —
// rewriting would desync from the /v1 route table).
type handlerAdapter struct{}

func (handlerAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := getHandler()
	if h == nil {
		http.Error(w, "ai runtime not initialized", http.StatusServiceUnavailable)
		return
	}
	h.ServeHTTP(w, r)
}

var (
	hmu        sync.RWMutex
	registered http.Handler
)

// SetHandler registers the ai runtime's public HTTP handler (typically
// beego.BeeApp.Handlers after routers/router.go init). Safe for
// concurrent use; pass nil to deactivate.
func SetHandler(h http.Handler) {
	hmu.Lock()
	registered = h
	hmu.Unlock()
}

func getHandler() http.Handler {
	hmu.RLock()
	h := registered
	hmu.RUnlock()
	return h
}
