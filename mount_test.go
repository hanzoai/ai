package ai_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/ai"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
)

func TestMountWithoutHandlerReturns503(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := ai.Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	ai.SetHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/ai/health", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}

func TestMountForwardsToRegisteredHandlerStripsPrefix(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := ai.Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	var sawPath string
	ai.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ai.SetHandler(nil)

	// /v1/ai/health → beego sees /v1/health
	req := httptest.NewRequest(http.MethodGet, "/v1/ai/health", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("body=%q want {\"ok\":true}", body)
	}
	if sawPath != "/v1/health" {
		t.Fatalf("sawPath=%q want /v1/health (prefix should strip /v1/ai to /v1)", sawPath)
	}
}

func TestMountForwardsRootRequest(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := ai.Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	var sawPath string
	ai.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer ai.SetHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/ai/", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if sawPath != "/v1/" {
		t.Fatalf("sawPath=%q want /v1/", sawPath)
	}
}
