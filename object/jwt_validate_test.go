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

package object

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// makeJWT builds a syntactically valid (UNSIGNED) JWT string carrying the given
// payload claims. Only the payload segment is meaningful for the iss/aud policy.
func makeJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	pj, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	body := base64.RawURLEncoding.EncodeToString(pj)
	return hdr + "." + body + ".sig"
}

// TestCheckIssAud_Issuer covers the issuer half of the policy, including the
// white-label multi-issuer SET (one binary trusts hanzo AND lux/zoo/pars).
func TestCheckIssAud_Issuer(t *testing.T) {
	want := []string{"https://hanzo.id"}
	if err := checkIssAud("https://hanzo.id", []string{"hanzo-console"}, want, nil); err != nil {
		t.Fatalf("correct issuer must pass, got %v", err)
	}
	if err := checkIssAud("https://evil.example", []string{"hanzo-console"}, want, nil); err != ErrJWTBadIssuer {
		t.Fatalf("wrong issuer must be rejected, got %v", err)
	}
	if err := checkIssAud("", nil, want, nil); err != ErrJWTBadIssuer {
		t.Fatalf("empty issuer must be rejected when an issuer is expected, got %v", err)
	}
	// Empty expectedIss set disables the issuer check (issuer-only rollout off).
	if err := checkIssAud("anything", nil, nil, nil); err != nil {
		t.Fatalf("empty expected issuer set must skip the check, got %v", err)
	}

	// WHITE-LABEL: a SET of trusted brand issuers accepts a token from ANY of them
	// (a lux token validates on the hanzo binary), and STILL rejects an outsider.
	brands := []string{"https://hanzo.id", "https://lux.id", "https://zoolabs.id", "https://pars.id"}
	for _, iss := range brands {
		if err := checkIssAud(iss, []string{"lux-cloud"}, brands, nil); err != nil {
			t.Errorf("brand issuer %q must pass in the trusted set, got %v", iss, err)
		}
	}
	if err := checkIssAud("https://attacker.id", []string{"lux-cloud"}, brands, nil); err != ErrJWTBadIssuer {
		t.Fatalf("issuer outside the brand set must be rejected, got %v", err)
	}
}

// TestCheckIssAud_Audience covers the audience allowlist half of the policy.
func TestCheckIssAud_Audience(t *testing.T) {
	iss := "https://hanzo.id"
	expIss := []string{iss}
	allow := []string{"hanzo-console", "hanzo-cloud"}

	// In-allowlist audience passes.
	if err := checkIssAud(iss, []string{"hanzo-console"}, expIss, allow); err != nil {
		t.Fatalf("allowed audience must pass, got %v", err)
	}
	// Out-of-allowlist audience is rejected.
	if err := checkIssAud(iss, []string{"attacker-app"}, expIss, allow); err != ErrJWTBadAudience {
		t.Fatalf("disallowed audience must be rejected, got %v", err)
	}
	// No audiences at all, with an allowlist set, is rejected (fail-secure).
	if err := checkIssAud(iss, nil, expIss, allow); err != ErrJWTBadAudience {
		t.Fatalf("missing audience must be rejected when allowlist set, got %v", err)
	}
	// Empty allowlist disables the audience check.
	if err := checkIssAud(iss, []string{"whatever"}, expIss, nil); err != nil {
		t.Fatalf("empty allowlist must skip audience check, got %v", err)
	}
}

// TestJwtAudienceAllowlist_BrandUnion proves the resolved audience allowlist ALWAYS
// includes every brand's <brand>-cloud aud (so a lux token validates on the
// request-auth path), whether it comes from a hanzo-only GATEWAY_ALLOWED_AUDIENCES
// env or the pinned jwtAudiences config — and never duplicates an existing entry.
func TestJwtAudienceAllowlist_BrandUnion(t *testing.T) {
	has := func(list []string, v string) bool {
		for _, s := range list {
			if s == v {
				return true
			}
		}
		return false
	}

	// A legacy hanzo-only env override must STILL accept lux-cloud (brand union),
	// with no duplicate of the env-supplied hanzo-cloud.
	os.Setenv("GATEWAY_ALLOWED_AUDIENCES", "hanzo-app,hanzo-console,hanzo-cloud")
	os.Unsetenv("IAM_AUDIENCE")
	os.Unsetenv("AUTH_AUDIENCE")
	defer os.Unsetenv("GATEWAY_ALLOWED_AUDIENCES")
	got := jwtAudienceAllowlist()
	for _, want := range []string{"hanzo-cloud", "lux-cloud", "zoo-cloud", "pars-cloud"} {
		if !has(got, want) {
			t.Errorf("hanzo-only env override must still accept %q, got %v", want, got)
		}
	}
	n := 0
	for _, s := range got {
		if s == "hanzo-cloud" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("hanzo-cloud must appear exactly once (no duplicate), got %d in %v", n, got)
	}
}

// TestJwtUnverifiedClaims decodes iss/aud directly from the payload, proving the
// raw-map decode avoids the iam.Claims embedded-field tag collision.
func TestJwtUnverifiedClaims(t *testing.T) {
	tok := makeJWT(t, map[string]interface{}{
		"iss":   "https://hanzo.id",
		"aud":   "hanzo-console",
		"owner": "hanzo",
	})
	iss, auds, err := jwtUnverifiedClaims(tok)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if iss != "https://hanzo.id" {
		t.Errorf("iss=%q, want https://hanzo.id", iss)
	}
	if len(auds) != 1 || auds[0] != "hanzo-console" {
		t.Errorf("aud=%v, want [hanzo-console]", auds)
	}

	// Array-form aud.
	tok2 := makeJWT(t, map[string]interface{}{"iss": "https://hanzo.id", "aud": []string{"a", "b"}})
	_, auds2, err := jwtUnverifiedClaims(tok2)
	if err != nil {
		t.Fatalf("decode array aud failed: %v", err)
	}
	if len(auds2) != 2 || auds2[0] != "a" || auds2[1] != "b" {
		t.Errorf("array aud=%v, want [a b]", auds2)
	}

	// Malformed tokens are rejected, never silently accepted.
	for _, bad := range []string{"", "onlyonepart", "a.b", "a.b.c.d", "x.@@@notbase64@@@.z"} {
		if _, _, err := jwtUnverifiedClaims(bad); err == nil {
			t.Errorf("malformed token %q must error", bad)
		}
	}
}

// TestValidateJWTIssAud_EndToEnd exercises the config-driven wrapper: a token
// minted by the trusted issuer passes; a wrong-issuer token is rejected.
func TestValidateJWTIssAud_EndToEnd(t *testing.T) {
	good := makeJWT(t, map[string]interface{}{"iss": "https://hanzo.id", "aud": "hanzo-console"})
	if err := ValidateJWTIssAud(good); err != nil {
		t.Fatalf("default-issuer token must pass, got %v", err)
	}
	bad := makeJWT(t, map[string]interface{}{"iss": "https://attacker.example", "aud": "hanzo-console"})
	if err := ValidateJWTIssAud(bad); err != ErrJWTBadIssuer {
		t.Fatalf("wrong-issuer token must be rejected, got %v", err)
	}
}

// TestExpectedJWTIssuerFromEnv proves the issuer is read from the ACTUAL deployed
// env keys (JWT_ISSUER/IAM_ISSUER/CLOUD_IAM_ISSUER/AUTH_ISSUER), closing the
// bug where the code read only the never-set `jwtIssuer` config.
func TestExpectedJWTIssuerFromEnv(t *testing.T) {
	t.Setenv("JWT_ISSUER", "")
	t.Setenv("IAM_ISSUER", "https://hanzo.id")
	t.Setenv("CLOUD_IAM_ISSUER", "")
	t.Setenv("AUTH_ISSUER", "")
	if got := expectedJWTIssuer(); got != "https://hanzo.id" {
		t.Errorf("issuer from IAM_ISSUER = %q, want https://hanzo.id", got)
	}
	// First non-empty in priority order wins (JWT_ISSUER before IAM_ISSUER).
	t.Setenv("JWT_ISSUER", "https://primary.example")
	if got := expectedJWTIssuer(); got != "https://primary.example" {
		t.Errorf("JWT_ISSUER must take priority, got %q", got)
	}
	// All unset => the https://hanzo.id default.
	t.Setenv("JWT_ISSUER", "")
	t.Setenv("IAM_ISSUER", "")
	if got := expectedJWTIssuer(); got != defaultJWTIssuer {
		t.Errorf("default issuer = %q, want %q", got, defaultJWTIssuer)
	}
}

// TestJwtAudienceAllowlistFromEnv proves the audience allowlist mirrors the
// gateway: GATEWAY_ALLOWED_AUDIENCES is the base, IAM_AUDIENCE + AUTH_AUDIENCE are
// folded in (deduped). This closes the env-key mismatch where the code read only
// the never-set `jwtAudiences` config, silently disabling the audience check.
func TestJwtAudienceAllowlistFromEnv(t *testing.T) {
	t.Setenv("GATEWAY_ALLOWED_AUDIENCES", "hanzo-app, hanzo-console , hanzo-cloud")
	t.Setenv("IAM_AUDIENCE", "hanzo-cloud") // already present => deduped
	t.Setenv("AUTH_AUDIENCE", "https://api.hanzo.ai")
	got := jwtAudienceAllowlist()

	want := map[string]bool{"hanzo-app": true, "hanzo-console": true, "hanzo-cloud": true, "https://api.hanzo.ai": true}
	if len(got) != len(want) {
		t.Fatalf("allowlist=%v, want %d unique entries", got, len(want))
	}
	seen := map[string]int{}
	for _, a := range got {
		if !want[a] {
			t.Errorf("unexpected audience %q in allowlist", a)
		}
		seen[a]++
	}
	if seen["hanzo-cloud"] != 1 {
		t.Errorf("hanzo-cloud (in both GATEWAY_ALLOWED_AUDIENCES and IAM_AUDIENCE) must be deduped, count=%d", seen["hanzo-cloud"])
	}
}

// TestForeignAudienceRejectedFromEnv is the R3 end-to-end assertion: with the
// allowlist sourced from the deployed env, a token carrying a FOREIGN aud is
// rejected while a legitimate console token passes — proving aud is enforced.
func TestForeignAudienceRejectedFromEnv(t *testing.T) {
	t.Setenv("JWT_ISSUER", "https://hanzo.id")
	t.Setenv("GATEWAY_ALLOWED_AUDIENCES", "hanzo-app,hanzo-console,hanzo-cloud,https://api.hanzo.ai")
	t.Setenv("IAM_AUDIENCE", "hanzo-cloud")

	foreign := makeJWT(t, map[string]interface{}{"iss": "https://hanzo.id", "aud": "evil-app"})
	if err := ValidateJWTIssAud(foreign); err != ErrJWTBadAudience {
		t.Fatalf("foreign-aud token MUST be rejected (R3), got %v", err)
	}
	for _, aud := range []string{"hanzo-console", "hanzo-cloud", "hanzo-app", "https://api.hanzo.ai"} {
		good := makeJWT(t, map[string]interface{}{"iss": "https://hanzo.id", "aud": aud})
		if err := ValidateJWTIssAud(good); err != nil {
			t.Errorf("legit aud %q must pass, got %v", aud, err)
		}
	}
}

// TestTrustedJWTIssuers_WhiteLabel proves the request-auth issuer set includes the
// primary (config/env) issuer PLUS every white-label brand issuer, so ONE binary
// validates hanzo AND lux/zoo/pars tokens. This is the multi-brand equivalent of
// the single expectedJWTIssuer.
func TestTrustedJWTIssuers_WhiteLabel(t *testing.T) {
	t.Setenv("JWT_ISSUER", "https://hanzo.id")
	t.Setenv("WHITELABEL_ISSUERS", "")
	got := trustedJWTIssuers()
	want := []string{"https://hanzo.id", "https://lux.id", "https://zoolabs.id", "https://pars.id"}
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("trusted issuer set %v missing brand issuer %q", got, w)
		}
	}

	// The WHITELABEL_ISSUERS override ADDS a brand without a rebuild.
	t.Setenv("WHITELABEL_ISSUERS", "https://custom.brand.id")
	if !contains(trustedJWTIssuers(), "https://custom.brand.id") {
		t.Errorf("WHITELABEL_ISSUERS override must add the custom issuer")
	}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// TestValidateJWTIssAud_LuxToken is THE white-label gate: a lux-brand token
// (iss=https://lux.id, aud=lux-cloud) validates on the same binary that validates
// a hanzo token, while an attacker-issuer token is still rejected. This proves the
// backend accepts lux without weakening the hanzo/foreign checks.
func TestValidateJWTIssAud_LuxToken(t *testing.T) {
	// The deployment's primary issuer is hanzo; the brand set adds lux.id.
	t.Setenv("JWT_ISSUER", "https://hanzo.id")
	t.Setenv("WHITELABEL_ISSUERS", "")
	// Audience allowlist must include lux-cloud (the config step of this change).
	t.Setenv("GATEWAY_ALLOWED_AUDIENCES", "hanzo-cloud,lux-cloud,zoo-cloud,pars-cloud")
	t.Setenv("IAM_AUDIENCE", "hanzo-cloud")

	// A real lux console token: iss=lux.id, aud=lux-cloud.
	luxTok := makeJWT(t, map[string]interface{}{"iss": "https://lux.id", "aud": "lux-cloud", "owner": "lux"})
	if err := ValidateJWTIssAud(luxTok); err != nil {
		t.Fatalf("lux token (iss=lux.id, aud=lux-cloud) MUST pass on the white-label binary, got %v", err)
	}

	// A hanzo token STILL passes (no regression).
	hanzoTok := makeJWT(t, map[string]interface{}{"iss": "https://hanzo.id", "aud": "hanzo-cloud", "owner": "hanzo"})
	if err := ValidateJWTIssAud(hanzoTok); err != nil {
		t.Fatalf("hanzo token MUST still pass (no regression), got %v", err)
	}

	// A lux-issuer token carrying a FOREIGN aud is still rejected (aud enforced).
	luxForeignAud := makeJWT(t, map[string]interface{}{"iss": "https://lux.id", "aud": "evil-app"})
	if err := ValidateJWTIssAud(luxForeignAud); err != ErrJWTBadAudience {
		t.Fatalf("lux-issuer token with foreign aud must be rejected on aud, got %v", err)
	}

	// An attacker-issuer token is rejected even with a valid brand aud.
	attackerTok := makeJWT(t, map[string]interface{}{"iss": "https://attacker.id", "aud": "lux-cloud"})
	if err := ValidateJWTIssAud(attackerTok); err != ErrJWTBadIssuer {
		t.Fatalf("attacker-issuer token must be rejected on iss, got %v", err)
	}
}
