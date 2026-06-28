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

// TestCheckIssAud_Issuer covers the issuer half of the policy.
func TestCheckIssAud_Issuer(t *testing.T) {
	const want = "https://hanzo.id"
	if err := checkIssAud(want, []string{"hanzo-console"}, want, nil); err != nil {
		t.Fatalf("correct issuer must pass, got %v", err)
	}
	if err := checkIssAud("https://evil.example", []string{"hanzo-console"}, want, nil); err != ErrJWTBadIssuer {
		t.Fatalf("wrong issuer must be rejected, got %v", err)
	}
	if err := checkIssAud("", nil, want, nil); err != ErrJWTBadIssuer {
		t.Fatalf("empty issuer must be rejected when an issuer is expected, got %v", err)
	}
	// Empty expectedIss disables the issuer check (issuer-only rollout off).
	if err := checkIssAud("anything", nil, "", nil); err != nil {
		t.Fatalf("empty expected issuer must skip the check, got %v", err)
	}
}

// TestCheckIssAud_Audience covers the audience allowlist half of the policy.
func TestCheckIssAud_Audience(t *testing.T) {
	const iss = "https://hanzo.id"
	allow := []string{"hanzo-console", "hanzo-cloud"}

	// In-allowlist audience passes.
	if err := checkIssAud(iss, []string{"hanzo-console"}, iss, allow); err != nil {
		t.Fatalf("allowed audience must pass, got %v", err)
	}
	// Out-of-allowlist audience is rejected.
	if err := checkIssAud(iss, []string{"attacker-app"}, iss, allow); err != ErrJWTBadAudience {
		t.Fatalf("disallowed audience must be rejected, got %v", err)
	}
	// No audiences at all, with an allowlist set, is rejected (fail-secure).
	if err := checkIssAud(iss, nil, iss, allow); err != ErrJWTBadAudience {
		t.Fatalf("missing audience must be rejected when allowlist set, got %v", err)
	}
	// Empty allowlist disables the audience check.
	if err := checkIssAud(iss, []string{"whatever"}, iss, nil); err != nil {
		t.Fatalf("empty allowlist must skip audience check, got %v", err)
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
