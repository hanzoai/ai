// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

type memSecrets struct {
	mu   sync.RWMutex
	vals map[string]string
}

func (m *memSecrets) GetSecret(_ context.Context, ref string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.vals[ref]
	if !ok {
		return nil, errors.New("secret not found")
	}
	return []byte(v), nil
}

func (m *memSecrets) PutSecret(_ context.Context, ref string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vals[ref] = string(value)
	return nil
}

func TestPipeToFamily_EnsoForwardsTenantKeyOverridesAndOpenRouterUsesDirectKey(t *testing.T) {
	store := &memSecrets{vals: map[string]string{
		"orgs/acme/ai-keys/openrouter":             "sk-or-acme-org",
		"orgs/acme/users/alice/ai-keys/openrouter": "sk-or-alice-user",
		"orgs/acme/ai-keys/nvidia":                 "nvapi-acme-org",
	}}
	object.SetSecretStore(store)
	object.ResetProviderKeyOverrideCache()
	t.Cleanup(func() {
		object.SetSecretStore(nil)
		object.ResetProviderKeyOverrideCache()
	})

	t.Setenv("ZEN_ADMIN_TOKEN", "test-admin-tok")
	// 1. Enso (first-party localhost server) receives BOTH X-Hanzo-Override-OpenRouter
	//    (user override beats org!) and X-Hanzo-Override-Nvidia (org override).
	var gotEnsoOR, gotEnsoNV string
	ensoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/admin/catalog" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":{"enso-pro":{"route":[{"provider":"nvidia","upstream":"nvidia/nemotron","cost":{"in":"0","out":"0"}}]}}}`))
			return
		}
		gotEnsoOR = r.Header.Get("X-Hanzo-Override-OpenRouter")
		gotEnsoNV = r.Header.Get("X-Hanzo-Override-Nvidia")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"enso-pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer ensoSrv.Close()

	restore(t, ensoFam)
	ensoFam.providerFn = func() *object.Provider {
		return &object.Provider{Name: "enso", Type: "Enso", ProviderUrl: ensoSrv.URL, ClientSecret: "svc-key"}
	}
	ensoFam.mu.Lock()
	ensoFam.byID = map[string]zenModel{"enso-pro": {ID: "enso-pro"}}
	ensoFam.loaded = true
	ensoFam.fetchedAt = time.Now()
	ensoFam.mu.Unlock()

	body := []byte(`{"model":"enso-pro","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	user := &iam.User{Owner: "acme", Name: "alice"}
	if refused := c.pipeToFamily(ensoFam, "chat/completions", "openai", "enso-pro", body, false, 1024, "acme", user, true, nil, time.Now()); refused != nil {
		t.Fatalf("enso pipeToFamily refused: %+v", refused)
	}
	if st := c.Fiber().Response().StatusCode(); st != http.StatusOK {
		t.Fatalf("enso status = %d, want 200", st)
	}
	if gotEnsoOR != "sk-or-alice-user" {
		t.Fatalf("enso X-Hanzo-Override-OpenRouter = %q, want sk-or-alice-user", gotEnsoOR)
	}
	if gotEnsoNV != "nvapi-acme-org" {
		t.Fatalf("enso X-Hanzo-Override-Nvidia = %q, want nvapi-acme-org", gotEnsoNV)
	}

	// 2. OpenRouter family uses the tenant's OpenRouter key directly in Authorization
	//    and NEVER sends X-Hanzo-Override-* headers upstream.
	var gotORAuth, gotORLeak1, gotORLeak2 string
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotORAuth = r.Header.Get("Authorization")
		gotORLeak1 = r.Header.Get("X-Hanzo-Override-OpenRouter")
		gotORLeak2 = r.Header.Get("X-Hanzo-Override-Nvidia")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-2","object":"chat.completion","model":"openai/gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer orSrv.Close()

	restore(t, openrouterFam)
	openrouterFam.providerFn = func() *object.Provider {
		return &object.Provider{Name: "openrouter", Type: "OpenRouter", ProviderUrl: orSrv.URL, ClientSecret: "sk-or-platform-drained"}
	}
	openrouterFam.mu.Lock()
	openrouterFam.byID = map[string]zenModel{"openai/gpt-5": {ID: "openai/gpt-5"}}
	openrouterFam.loaded = true
	openrouterFam.fetchedAt = time.Now()
	openrouterFam.mu.Unlock()

	orBody := []byte(`{"model":"openai/gpt-5","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	c2 := visit(http.MethodPost, "/v1/chat/completions")
	c2.Fiber().Request().SetBody(orBody)
	if refused := c2.pipeToFamily(openrouterFam, "chat/completions", "openai", "openai/gpt-5", orBody, false, 1024, "acme", user, true, nil, time.Now()); refused != nil {
		t.Fatalf("openrouter pipeToFamily refused: %+v", refused)
	}
	if st := c2.Fiber().Response().StatusCode(); st != http.StatusOK {
		t.Fatalf("openrouter status = %d, want 200", st)
	}
	if gotORAuth != "Bearer sk-or-alice-user" {
		t.Fatalf("openrouter Authorization = %q, want Bearer sk-or-alice-user", gotORAuth)
	}
	if gotORLeak1 != "" || gotORLeak2 != "" {
		t.Fatalf("override headers leaked to OpenRouter: %q / %q", gotORLeak1, gotORLeak2)
	}
}
