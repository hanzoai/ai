package ai

// TEMPORARY measurement harness (scratch; not for commit).

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

func TestZZZMeasureSurface(t *testing.T) {
	dir, _ := os.MkdirTemp("", "surface-*")
	cfg := &cloud.Config{Brand: "hanzo", Domain: "api.hanzo.ai", DataDir: dir}
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: deps.Logger, DisableStartupMessage: true})
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	type R struct{ Method, Path string }
	var routes []R
	zipOpenAPI, zipMCP := false, false
	for _, r := range app.Fiber().GetRoutes(true) {
		routes = append(routes, R{r.Method, r.Path})
		switch r.Path {
		case "/.well-known/openapi.json", "/openapi.json":
			zipOpenAPI = true
		case "/mcp":
			zipMCP = true
		}
	}
	b, _ := json.Marshal(map[string]any{"service": "ai", "live_routes": len(routes),
		"zip_openapi_route": zipOpenAPI, "zip_mcp_route": zipMCP, "routes": routes})
	os.WriteFile("/home/z/.cache/wf-typedsurface/ai_live.json", b, 0o644)
	t.Logf("SVC=ai LIVE=%d ZIP_OPENAPI=%v ZIP_MCP=%v", len(routes), zipOpenAPI, zipMCP)
}
