// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
)

// withStore gives a test a REAL store: SQLite in a temp directory, with every
// model synced, in about 20ms.
//
// It is a store and not a stand-in for one. A handler tested against a double
// proves that the double was called; tested against this it proves the row it
// wrote can be read back, which is the only version of the claim worth making.
// SQLite is what this deployment runs for a single instance anyway, so the
// dialect under test is a dialect in use rather than one chosen for tests.
func withStore(t *testing.T) {
	t.Helper()
	t.Setenv("driverName", "sqlite")
	t.Setenv("dataSourceName", filepath.Join(t.TempDir(), "store.db"))
	object.InitConfig()
}

// asUser stands up the ONE thing a principal cannot be built without: IAM, which
// is where an sk- key is exchanged for the person it belongs to. It is an
// external service, so it is doubled here — and only it. Everything the handler
// then does happens for real.
//
// Returns the credential to hand the handler.
func asUser(t *testing.T, user *iam.User) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/keys/principal" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if id, secret, ok := r.BasicAuth(); !ok || id == "" || secret == "" {
			// IAM derives the calling APP from Basic credentials alone; without
			// them it yields no principal at all. Held to that here so a test
			// cannot pass through a door production does not open.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "code": "unauthorized", "msg": "authentication required"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": user})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-test")
	t.Setenv("IAM_CLIENT_SECRET", "test-secret")
	return "Bearer sk-test-key"
}

// seedDefaultStore gives the deployment the one row a chat cannot be created
// without: add-chat resolves the default store for "admin" when the request names
// none, and refuses when there is not one. Seeded rather than stubbed, so the
// handler does the lookup it does in production.
func seedDefaultStore(t *testing.T) *object.Store {
	t.Helper()
	s := &object.Store{
		Owner:       "admin",
		Name:        "default",
		DisplayName: "Default Store",
		CreatedTime: "2026-01-01T00:00:00Z",
		IsDefault:   true,
	}
	if _, err := object.AddStore(s); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return s
}
