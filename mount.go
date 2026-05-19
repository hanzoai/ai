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

// Mount registers AI's HTTP surface per HIP-0106.
//
// Routes under /v1/ai/* are forwarded to the registered handler (the
// beego ControllerRegister built by routers/router.go). If no handler is
// registered yet, the routes 503 — this lets the cloud binary boot the
// ai subsystem progressively (load model config, initialize providers,
// then call SetHandler).
//
// The standalone cmd/ai/main.go shim calls SetHandler(beego.BeeApp.Handlers)
// after object.InitDb and routers/init. The unified binary calls the
// same SetHandler in its bootstrap.
func Mount(app *zip.App, deps cloud.Deps) error {
	log := deps.Logger
	if log == nil {
		log = luxlog.New("module", "ai")
	}
	log.Info("ai: mounting routes", "prefix", "/v1/ai")

	app.All("/v1/ai/*", zip.AdaptNetHTTP(handlerAdapter{}))
	return nil
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
