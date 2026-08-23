// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/object"
)

// withStore gives a test a REAL store: SQLite in a temp directory, with every
// model synced, in about 20ms.
//
// It is a store and not a stand-in for one. A handler tested against a double
// proves the double was called; tested against this it proves the row it wrote
// can be read back, which is the only version of the claim worth making. SQLite
// is what a single instance runs anyway, so the dialect under test is one in use
// rather than one chosen for tests.
func withStore(t *testing.T) {
	t.Helper()
	t.Setenv("driverName", "sqlite")
	t.Setenv("dataSourceName", filepath.Join(t.TempDir(), "store.db"))
	object.InitConfig()
}

// people is the IAM double's registry: one key per person, so a test can hold
// more than one of them at a time.
//
// It exists because IAM_URL is a single value. An earlier version of this helper
// stood up a server per user and pointed IAM_URL at the newest, which quietly
// made every credential in the test resolve to whoever was created last — so a
// test could believe it was refusing Bob while asking as Bob throughout, and pass
// for a reason it never stated. One server, keyed.
type people struct {
	mu sync.Mutex
	by map[string]*iam.User
	n  int
}

// asUser registers a person with the IAM double and returns the credential that
// resolves to them.
//
// IAM is the ONE thing doubled here, because an sk- key is exchanged for its
// principal there and nowhere else. Everything the handler then does happens for
// real. The double holds to Basic credentials the way IAM does — it derives the
// calling app from them alone — so a test cannot pass through a door production
// keeps shut.
func (p *people) asUser(t *testing.T, user *iam.User) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	key := fmt.Sprintf("sk-test-%d", p.n)
	p.by[key] = user
	return "Bearer " + key
}

// withIAM stands up the double for one test and points the resolver at it.
func withIAM(t *testing.T) *people {
	t.Helper()
	p := &people{by: map[string]*iam.User{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/iam/keys/principal" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if id, secret, ok := r.BasicAuth(); !ok || id == "" || secret == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "code": "unauthorized", "msg": "authentication required"})
			return
		}
		p.mu.Lock()
		user, known := p.by[r.URL.Query().Get("accessKey")]
		p.mu.Unlock()
		if !known {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "code": "key_unknown", "msg": "the entity does not exist"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": user})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-test")
	t.Setenv("IAM_CLIENT_SECRET", "test-secret")
	return p
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
