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

package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	beecontext "github.com/hanzoai/beego/context"
	"github.com/hanzoai/ai/object"
	iam "github.com/hanzoai/iam"
)

// ── adminProviderView projection ────────────────────────────────────────────

// TestToAdminProviderView_FieldsAndFlags verifies the projection maps State →
// enabled, IsDefault → primary, resolves keyPresent as a boolean, and carries the
// model count from the supplied map.
func TestToAdminProviderView_FieldsAndFlags(t *testing.T) {
	// KMS disabled + no env key → a kms:// ref is fail-closed absent.
	t.Setenv("KMS_CLIENT_ID", "")
	t.Setenv("KMS_SERVICE_TOKEN", "")
	t.Setenv("HANZO_API_KEY", "")

	p := &object.Provider{
		Name:         "do-ai",
		DisplayName:  "DigitalOcean AI (GenAI)",
		Category:     "Model",
		Type:         "DigitalOcean",
		ProviderUrl:  "https://inference.do-ai.run/v1",
		ClientSecret: "kms://DO_AI_API_KEY_MISSING_FOR_TEST",
		State:        "Active",
		IsDefault:    true,
	}
	counts := map[string]int{"do-ai": 122}

	v := toAdminProviderView(p, counts)

	if v.Name != "do-ai" {
		t.Errorf("Name = %q, want do-ai", v.Name)
	}
	if v.DisplayName != "DigitalOcean AI (GenAI)" {
		t.Errorf("DisplayName = %q", v.DisplayName)
	}
	if v.Type != "DigitalOcean" {
		t.Errorf("Type = %q, want DigitalOcean", v.Type)
	}
	if !v.Enabled {
		t.Error("Enabled = false for State=Active, want true")
	}
	if !v.Primary {
		t.Error("Primary = false for IsDefault=true, want true")
	}
	if v.KeyPresent {
		t.Error("KeyPresent = true for an unresolved kms:// ref (KMS off), want false")
	}
	if v.ModelCount != 122 {
		t.Errorf("ModelCount = %d, want 122", v.ModelCount)
	}
	if v.ProviderURL != "https://inference.do-ai.run/v1" {
		t.Errorf("ProviderURL = %q", v.ProviderURL)
	}
}

func TestToAdminProviderView_DisabledAndKeyFromEnv(t *testing.T) {
	// Env-first resolution → keyPresent true; State != Active → enabled false;
	// IsDefault false → primary false.
	t.Setenv("FIREWORKS_API_KEY_TEST_PRESENT", "fw-key")
	p := &object.Provider{
		Name:         "fireworks",
		Category:     "Model",
		Type:         "Fireworks",
		ClientSecret: "kms://FIREWORKS_API_KEY_TEST_PRESENT",
		State:        "Disabled",
		IsDefault:    false,
	}
	v := toAdminProviderView(p, map[string]int{})
	if v.Enabled {
		t.Error("Enabled = true for State=Disabled, want false")
	}
	if v.Primary {
		t.Error("Primary = true for IsDefault=false, want false")
	}
	if !v.KeyPresent {
		t.Error("KeyPresent = false for a kms:// ref resolvable via env, want true")
	}
	if v.ModelCount != 0 {
		t.Errorf("ModelCount = %d for a provider absent from the count map, want 0", v.ModelCount)
	}
}

// TestAdminProviderView_NeverSerializesSecrets is the security regression guard:
// the JSON of the management view must NOT contain any secret-bearing key. If a
// future edit adds ClientSecret/ProviderKey to the struct, this fails.
func TestAdminProviderView_NeverSerializesSecrets(t *testing.T) {
	t.Setenv("SUPER_SECRET_TEST_KEY", "sk-must-not-leak-123")
	p := &object.Provider{
		Name:         "do-ai",
		Category:     "Model",
		ClientSecret: "kms://SUPER_SECRET_TEST_KEY",
		ProviderKey:  "sk-provider-key-must-not-leak",
		UserKey:      "user-key-must-not-leak",
		SignKey:      "sign-key-must-not-leak",
		State:        "Active",
	}
	v := toAdminProviderView(p, map[string]int{"do-ai": 1})
	// Sanity: env-first means keyPresent should be true here.
	if !v.KeyPresent {
		t.Fatal("precondition: expected keyPresent true via env resolution")
	}
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(blob)
	for _, forbidden := range []string{
		"sk-must-not-leak-123",          // resolved kms value
		"kms://SUPER_SECRET_TEST_KEY",   // the raw ref
		"sk-provider-key-must-not-leak", // providerKey
		"user-key-must-not-leak",        // userKey
		"sign-key-must-not-leak",        // signKey
		"clientSecret", "providerKey",   // secret-bearing field names
		"userKey", "signKey", "configText",
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("admin provider view JSON leaked %q: %s", forbidden, s)
		}
	}
	// Positive: the boolean signal IS present.
	if !strings.Contains(s, `"keyPresent":true`) {
		t.Errorf("expected keyPresent:true in JSON, got: %s", s)
	}
}

// Note: the exactly-one-primary transition now lives in object.SetPrimaryModelProvider
// (one atomic, promote-first UPDATE pair — no in-memory diff to loop over). Its
// ordering + WHERE clauses are regression-tested in
// object/provider_default_atomic_test.go against the built SQL, without a DB.

// ── C-1 controller self-guard (RequireSuperAdmin) ──────────────────────────
//
// Belt-AND-suspenders: even if the authz filter is ever bypassed, the three admin
// controllers call c.RequireSuperAdmin() FIRST and refuse. These tests exercise
// that guard directly (session principal), asserting fail-closed 401/403 and pass
// for a super admin — the SAME policy as the filter (util.IsSuperAdmin).

// ctrlFakeSession (the minimal in-memory session.Store used to carry a test
// principal without a live session manager) is defined once in index_auth_test.go
// and shared across the package's controller tests — reused here to keep a single
// definition (it collided when this file, from the provider-admin PR, was rebased
// onto the branch that introduced index_auth_test.go).

// newGuardController builds an ApiController with an httptest recorder and a session
// optionally carrying `user` as the principal.
func newGuardController(user *iam.User) (*ApiController, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	ctx := beecontext.NewContext()
	ctx.Reset(rec, httptest.NewRequest("POST", "/v1/admin/providers/toggle", nil))
	sess := &ctrlFakeSession{data: map[interface{}]interface{}{}}
	if user != nil {
		sess.data["user"] = iam.Claims{User: *user}
	}
	ctx.Input.CruSession = sess
	c := &ApiController{}
	c.Init(ctx, "ApiController", "ToggleAdminProvider", nil)
	return c, rec
}

func TestRequireSuperAdmin_NoPrincipal401(t *testing.T) {
	c, rec := newGuardController(nil)
	if c.RequireSuperAdmin() {
		t.Error("RequireSuperAdmin() = true for no principal, want false (deny)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no-principal status = %d, want 401", rec.Code)
	}
}

func TestRequireSuperAdmin_OrgAdminForbidden403(t *testing.T) {
	orgAdmin := &iam.User{Owner: "maxpower", Name: "dave", IsAdmin: true}
	c, rec := newGuardController(orgAdmin)
	if c.RequireSuperAdmin() {
		t.Error("RequireSuperAdmin() = true for an ORG admin, want false (not a platform admin)")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("org-admin status = %d, want 403", rec.Code)
	}
}

func TestRequireSuperAdmin_SuperAdminPasses(t *testing.T) {
	superAdmin := &iam.User{Owner: "admin", Name: "admin", IsAdmin: true}
	c, rec := newGuardController(superAdmin)
	if !c.RequireSuperAdmin() {
		t.Error("RequireSuperAdmin() = false for a super admin, want true (pass)")
	}
	// Pass path writes nothing (the handler continues).
	if rec.Body.Len() != 0 {
		t.Errorf("super-admin pass wrote a body %q, want none", rec.Body.String())
	}
}

// ── stateForEnabled ─────────────────────────────────────────────────────────

func TestStateForEnabled(t *testing.T) {
	if got := stateForEnabled(true); got != "Active" {
		t.Errorf("stateForEnabled(true) = %q, want Active", got)
	}
	if got := stateForEnabled(false); got != "Disabled" {
		t.Errorf("stateForEnabled(false) = %q, want Disabled", got)
	}
}

// ── modelCountByProvider ────────────────────────────────────────────────────

// TestModelCountByProvider_MatchesStaticMap verifies the count helper agrees
// with an independent count over the static map — proving it is computed from the
// route table, not hardcoded. (In the unit env no YAML config is loaded, so the
// helper uses the static modelRoutes map.)
func TestModelCountByProvider_MatchesStaticMap(t *testing.T) {
	if GetModelConfig() != nil {
		t.Skip("YAML model config loaded; static-map parity check not applicable")
	}
	got := modelCountByProvider()

	// Independent recount over the same source.
	want := map[string]int{}
	for _, route := range modelRoutes {
		want[route.providerName]++
	}

	if len(got) != len(want) {
		t.Errorf("provider count keys = %d, want %d (got=%v want=%v)", len(got), len(want), got, want)
	}
	for prov, n := range want {
		if got[prov] != n {
			t.Errorf("modelCountByProvider()[%q] = %d, want %d", prov, got[prov], n)
		}
	}

	// Every count must equal the number of routes whose providerName == prov —
	// asserted here for the known providers so drift in the table is visible.
	for _, prov := range []string{"do-ai", "fireworks", "openai-direct"} {
		n := 0
		for _, route := range modelRoutes {
			if route.providerName == prov {
				n++
			}
		}
		if got[prov] != n {
			t.Errorf("count for %q = %d, recount = %d", prov, got[prov], n)
		}
		if n == 0 {
			t.Errorf("expected at least one route for provider %q", prov)
		}
	}
}

// TestModelCountByProvider_ZenAttributedToServingProvider documents that branded
// zen models are counted under their SERVING provider (do-ai), so a "zen"
// provider record (whose name no route targets) reports 0 — accurate, since the
// zen MODELS route via do-ai. This locks in the documented behavior.
func TestModelCountByProvider_ZenAttributedToServingProvider(t *testing.T) {
	if GetModelConfig() != nil {
		t.Skip("YAML model config loaded; static-map behavior check not applicable")
	}
	counts := modelCountByProvider()
	if _, ok := counts["zen"]; ok {
		t.Errorf("did not expect any route with providerName=\"zen\" (zen models route via do-ai); got count=%d", counts["zen"])
	}
	// zen models exist and route through do-ai — confirm at least one zen key
	// resolves to the do-ai provider so the attribution rationale holds.
	if r := resolveModelRoute("zen4"); r == nil || r.providerName != "do-ai" {
		t.Errorf("zen4 route = %+v, want providerName=do-ai", r)
	}
}
