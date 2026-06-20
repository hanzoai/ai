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
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
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
	log.Info("ai: initializing runtime")
	if err := Bootstrap(); err != nil {
		return err
	}
	log.Info("ai: runtime initialized")
	return nil
}

// mountRoutes wires AI's /v1/ai/* prefix onto the zip app. It has no
// infrastructure dependencies (no DB, no providers) and is what the route
// adapter tests exercise; Mount adds runtime init on top.
func mountRoutes(app *zip.App) {
	app.All("/v1/ai/*", zip.AdaptNetHTTP(handlerAdapter{}))
}

func init() {
	cloud.Register("ai", 150, func(app any, deps cloud.Deps) error {
		return Mount(app.(*zip.App), deps)
	})
}

// handlerAdapter forwards each request under /v1/ai/* to the registered
// runtime handler (beego ControllerRegister) or returns 503 if none.
type handlerAdapter struct{}

func (handlerAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := getHandler()
	if h == nil {
		http.Error(w, "ai runtime not initialized", http.StatusServiceUnavailable)
		return
	}
	// Strip the /v1/ai prefix before delegating so beego routes that are
	// registered at /v1/* (the existing route table) match unchanged.
	r2 := *r
	if u := *r.URL; true {
		// trim /v1/ai prefix; keep the leading /v1/ for beego routes.
		const prefix = "/v1/ai"
		if len(u.Path) >= len(prefix) && u.Path[:len(prefix)] == prefix {
			u.Path = "/v1" + u.Path[len(prefix):]
			if u.Path == "/v1" {
				u.Path = "/v1/"
			}
		}
		r2.URL = &u
	}
	h.ServeHTTP(w, &r2)
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
