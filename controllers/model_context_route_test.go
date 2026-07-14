// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"os"
	"path/filepath"
	"testing"
)

// The same model is served at different sizes by different providers: GLM-5.2
// is a 1M-context model, but DigitalOcean serves it at 262144. The window is
// therefore a property of the (provider, upstream) pair, and a prompt too big
// for one provider should be routed to one that can hold it — not refused.
const ctxRouteYAML = `
version: 1
models:
  # The model default: what glm-5.2 IS.
  glm-5.2:
    provider: do-ai
    upstream: glm-5.2
    context_window: 262144   # ...but this is what DO serves it at.

  # A provider that serves the SAME model with its full window.
  glm-5.2-wide:
    provider: nebius
    upstream: glm-5.2
    context_window: 1048576

  zen5:
    provider: do-ai
    upstream: glm-5.2
    fallbacks:
      - { provider: nebius, upstream: glm-5.2, context_window: 1048576 }

  # No window declared anywhere in the chain.
  mystery:
    provider: do-ai
    upstream: some-new-model
`

func loadCtxRouteConfig(t *testing.T) *ModelConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(ctxRouteYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		stopCh:  make(chan struct{}),
	}
	if err := mc.loadFromFile(path); err != nil {
		t.Fatal(err)
	}
	return mc
}

func TestContextWindow_AliasInheritsUpstream(t *testing.T) {
	mc := loadCtxRouteConfig(t)

	// zen5 declares no window; it inherits what its upstream is served at.
	if got := mc.ContextWindow("zen5"); got != 262144 {
		t.Errorf("zen5 should inherit glm-5.2's served window 262144, got %d", got)
	}
	// Undeclared anywhere → 0, and the caller applies the floor.
	if got := mc.ContextWindow("mystery"); got != 0 {
		t.Errorf("undeclared model: want 0 (caller uses the floor), got %d", got)
	}
}

func TestRouteForContext_FitsPrimary(t *testing.T) {
	mc := loadCtxRouteConfig(t)

	// A prompt the primary can hold stays on the primary.
	prov, up, win, ok := mc.RouteForContext("zen5", 100_000)
	if !ok {
		t.Fatal("zen5 should resolve")
	}
	if prov != "do-ai" || up != "glm-5.2" || win != 262144 {
		t.Errorf("a prompt that fits must stay on the primary: got provider=%s upstream=%s window=%d", prov, up, win)
	}
}

func TestRouteForContext_RerouteToProviderThatFits(t *testing.T) {
	mc := loadCtxRouteConfig(t)

	// 400K exceeds what DO serves glm-5.2 at (262144). The request must NOT be
	// refused — it must be routed to the provider that serves the same model
	// with a window big enough to hold it.
	prov, up, win, ok := mc.RouteForContext("zen5", 400_000)
	if !ok {
		t.Fatal("zen5 should resolve")
	}
	if prov != "nebius" {
		t.Errorf("a 400K prompt must reroute to the provider that can serve it: got provider=%s (window %d)", prov, win)
	}
	if up != "glm-5.2" {
		t.Errorf("rerouting must keep the SAME model, got upstream=%s", up)
	}
	if win != 1048576 {
		t.Errorf("window should be the fallback provider's: want 1048576, got %d", win)
	}
}

func TestRouteForContext_NothingFitsKeepsPrimary(t *testing.T) {
	mc := loadCtxRouteConfig(t)

	// Bigger than every provider in the chain: return the primary and let the
	// upstream reject it. An honest error from the model beats a guess from us.
	prov, _, _, ok := mc.RouteForContext("zen5", 5_000_000)
	if !ok {
		t.Fatal("zen5 should resolve")
	}
	if prov != "do-ai" {
		t.Errorf("when nothing fits, keep the primary and let the upstream speak: got %s", prov)
	}
}

func TestRouteForContext_UnknownSizeTakesPrimary(t *testing.T) {
	mc := loadCtxRouteConfig(t)

	// tokens <= 0 means "size unknown" — no basis to reroute.
	prov, _, _, ok := mc.RouteForContext("zen5", 0)
	if !ok || prov != "do-ai" {
		t.Errorf("unknown prompt size must take the primary, got provider=%s ok=%v", prov, ok)
	}
}
