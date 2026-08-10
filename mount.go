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
	"sync"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/fiber/v3/middleware/adaptor"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/conf"
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
func Mount(app *zip.App, deps cloud.Deps) error {
	log := luxlog.Default()
	if log == nil {
		log = luxlog.New("module", "ai")
	}
	log.Info("ai: mounting routes", "prefix", "/v1/ai")

	// Bind the EMBEDDED KMS. cloud holds luxfi/kms in-process (apps/kms), so a
	// provider's "kms://NAME" resolves through a function call against the store
	// this binary already has open — no HTTP, no hostname, no standalone
	// deployment. deps.KMS was already being handed to us and thrown away, while
	// ai kept its own HTTP client pointed at kms.hanzo.ai; that client 404'd on
	// the path it used and 401'd on the correct one, so every kms:// reference
	// was resolving from an env var instead. See object/kms.go.
	object.SetSecretStore(deps.KMS)

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
func mountRoutes(app *zip.App) {
	// Carry the request context ACROSS the zip→net/http boundary so the OTel SERVER
	// span cloud stashed on the request (TracingMiddleware → zip SetContext, the Fiber
	// user-context) reaches the ai handler. The plain zip.AdaptNetHTTP path uses
	// adaptor.HTTPHandler, which DROPS the Fiber user-context at the fasthttp→net/http
	// conversion — so controllers.emitGenAISpan started the gen_ai generation span from
	// a span-less context and it ORPHANED a fresh trace root instead of nesting under
	// the request span. o11y Observe then rendered every LLM call as an empty,
	// observation-less trace. HTTPHandlerWithContext stows c.Context() on the request;
	// handlerAdapter.ServeHTTP restores it onto r.Context() (below), so the generation
	// span nests as a CHILD of the request server span. One boundary, one carry.
	wrapped := adaptor.HTTPHandlerWithContext(handlerAdapter{})
	relay := func(c *zip.Ctx) error { return wrapped(c.Fiber()) }

	// NAME what the glob would otherwise swallow. Each promoted address is
	// registered on the HOST's router at its real pattern, pointing at the SAME
	// relay the glob below uses — so the request takes the identical path through
	// the identical handler and only the host's ability to DESCRIBE it changes.
	//
	// This is the seam that lets an address be described without being
	// reimplemented. A glob tells the host a prefix and nothing under it, so the
	// fleet document printed `/v1/{wildcard1}` where the model catalogue and the
	// limited-preview access flow are: three operations that were authenticated,
	// reachable and answering in production while no generated SDK, no MCP tool and
	// no CLI command existed for any of them, and while nothing said who served
	// them. One address, one owner, one implementation — and now one description.
	//
	// /v1/models/providers joined them later and for the weaker of the two
	// reasons — OWNERSHIP, not absence. Measured on the live document, it was
	// already published: the emitter picks a path up either way, and an
	// unpromoted one is attributed to the module path
	// (`x-app: github.com/hanzoai/ai`, 314 operations) rather than to the app
	// (`x-app: ai`). Promoting it puts the providers projection under the same
	// owner as the catalogue it projects, which is the whole point of it being a
	// sub-resource of /v1/models. Do NOT read this list as "these would 404
	// otherwise" — promotion has never changed what is served, only who the
	// document says serves it.
	register := map[string]func(string, ...zip.Handler) zip.Router{
		http.MethodGet:    app.Get,
		http.MethodPost:   app.Post,
		http.MethodPut:    app.Put,
		http.MethodPatch:  app.Patch,
		http.MethodDelete: app.Delete,
	}
	patterns := routers.App.Patterns()
	for _, pattern := range promoted {
		for _, method := range patterns[pattern] {
			if add, ok := register[method]; ok {
				add(pattern, relay)
			}
			// A verb with no host registrar keeps reaching the glob, exactly as it
			// did before. Promotion is additive: it can leave an address as
			// undescribed as it already was, never less served than it already was.
		}
	}

	app.All("/v1/*", relay)
}

// promoted is the subset of this service's route table whose addresses the host
// names on its own router. The methods are NOT listed: they are read from
// [routers.App.Patterns], the live table, so this can never claim a verb the
// service does not serve — and an entry naming a pattern the table does not hold
// registers nothing and fails TestPromotedAddressesExistInTheRouteTable.
//
// WHY A SUBSET, and it is a safety boundary rather than a backlog. This service
// registers ~190 addresses at BARE /v1/ paths, and the host mounts ~110 other
// subsystems in the same namespace. Today the glob is registered LAST and is the
// broadest pattern, so every one of those subsystems wins its own addresses by
// specificity. Promoting an address turns it into a SPECIFIC route, and a
// specific route that another subsystem also claims is decided by registration
// order — silently. Promoting the whole table would need the host to refuse a
// duplicate address at compose time, and it cannot yet.
//
// So an address joins this list when it is known not to be claimed elsewhere.
// These are: nothing else in the fleet registers /v1/models or anything under it.
var promoted = []string{
	"/v1/models",
	"/v1/models/providers",
	"/v1/models/:model/access",
}

// handlerAdapter forwards each request under /v1/* to the registered runtime
// handler (the the router ControllerRegister) or returns 503 if none. The path is
// passed through unchanged: the router's routes are registered at the same bare /v1/*
// paths the gateway forwards, so no rewrite is needed (and none must happen —
// rewriting would desync from the /v1 route table).
type handlerAdapter struct{}

func (handlerAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := getHandler()
	if h == nil {
		http.Error(w, "ai runtime not initialized", http.StatusServiceUnavailable)
		return
	}
	// Restore the caller's request context — carrying cloud's OTel SERVER span —
	// that adaptor.HTTPHandlerWithContext stowed on the request (mountRoutes), so the
	// gen_ai generation span emitted downstream (controllers.emitGenAISpan) nests
	// under the request span instead of orphaning a new trace root. Absent (a
	// non-adapted caller, or a request that never opened a server span) leaves r
	// untouched — never a panic, never a regression.
	if ctx, ok := adaptor.LocalContextFromHTTPRequest(r); ok && ctx != nil {
		r = r.WithContext(ctx)
	}
	h.ServeHTTP(w, r)
}

var (
	hmu        sync.RWMutex
	registered http.Handler
)

// SetHandler registers the ai runtime's public HTTP handler (typically
// web.BeeApp.Handlers after routers/router.go init). Safe for
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
