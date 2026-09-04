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
	"os"
	"strings"
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

// TestTokenIsOwnBrand proves the god-view brand gate primitive: a token is
// own-brand ONLY when its iss equals expectedJWTIssuer() — a SIBLING white-label
// brand issuer (trusted for sign-in via trustedJWTIssuers) is NOT own-brand, and a
// missing/malformed iss is fail-secure false. This is what keeps one brand's
// all-customer spend unreadable by another brand's admin token.
func TestTokenIsOwnBrand(t *testing.T) {
	t.Setenv("JWT_ISSUER", "https://hanzo.id") // deployment's own primary issuer

	// Sanity: lux.id is a TRUSTED sign-in issuer (so this is a real distinction,
	// not an already-rejected token).
	trusted := false
	for _, iss := range trustedJWTIssuers() {
		if iss == "https://lux.id" {
			trusted = true
		}
	}
	if !trusted {
		t.Fatal("precondition: https://lux.id must be a trusted sign-in issuer for this test to mean anything")
	}

	own := makeJWT(t, map[string]interface{}{"iss": "https://hanzo.id", "owner": "admin"})
	if !TokenIsOwnBrand(own) {
		t.Error("own-brand token (iss=hanzo.id) must be own-brand")
	}
	sibling := makeJWT(t, map[string]interface{}{"iss": "https://lux.id", "owner": "admin"})
	if TokenIsOwnBrand(sibling) {
		t.Error("sibling-brand token (iss=lux.id) must NOT be own-brand — even though its issuer is trusted for sign-in")
	}
	noIss := makeJWT(t, map[string]interface{}{"owner": "admin"})
	if TokenIsOwnBrand(noIss) {
		t.Error("token with no iss must be fail-secure NOT own-brand")
	}
	for _, bad := range []string{"", "a.b", "not-a-jwt"} {
		if TokenIsOwnBrand(bad) {
			t.Errorf("malformed token %q must be fail-secure NOT own-brand", bad)
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
// folded in (deduped), AND every white-label brand cloud aud (<brand>-cloud) is
// ALWAYS unioned in so one binary accepts a lux/zoo/pars bearer. This closes the
// env-key mismatch where the code read only the never-set `jwtAudiences` config,
// silently disabling the audience check.
func TestJwtAudienceAllowlistFromEnv(t *testing.T) {
	t.Setenv("GATEWAY_ALLOWED_AUDIENCES", "hanzo-app, hanzo-console , hanzo-cloud")
	t.Setenv("IAM_AUDIENCE", "hanzo-cloud") // already present => deduped
	t.Setenv("AUTH_AUDIENCE", "https://api.hanzo.ai")
	got := jwtAudienceAllowlist()

	// Every env-derived entry survives, whitespace trimmed. The bare host is carried
	// through as written — it names no brand, so it is not a brand audience.
	seen := map[string]int{}
	for _, a := range got {
		seen[a]++
	}
	// hanzo-ai is this plane's own audience and is always on an enforced allowlist.
	for _, want := range []string{"hanzo-app", "hanzo-console", "hanzo-cloud", "https://api.hanzo.ai", "hanzo-ai"} {
		if seen[want] != 1 {
			t.Errorf("env audience %q must appear exactly once, count=%d in %v", want, seen[want], got)
		}
	}

	// Each env app is mirrored onto every sibling brand, and nothing else appears:
	// an entry is legitimate only if the env named it or it is a known brand's
	// spelling of an app the env named.
	allowed := map[string]bool{}
	for _, e := range []string{"hanzo-app", "hanzo-console", "hanzo-cloud", "https://api.hanzo.ai", "hanzo-ai"} {
		allowed[e] = true
		if _, app, ok := splitBrandApp(e); ok {
			for _, b := range brandNames {
				allowed[b+"-"+app] = true
				if seen[b+"-"+app] != 1 {
					t.Errorf("app %q must be allowed for brand %q exactly once, count=%d in %v",
						app, b, seen[b+"-"+app], got)
				}
			}
		}
	}
	for a, n := range seen {
		if !allowed[a] {
			t.Errorf("unexpected audience %q in allowlist %v", a, got)
		}
		if n != 1 {
			t.Errorf("audience %q must be deduped, count=%d", a, n)
		}
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

// deployedAudiences is the GATEWAY_ALLOWED_AUDIENCES value hanzo-k8s deploy/cloud
// actually carried when lux.chat went dark (read 2026-08-10). It enumerates this
// deployment's OWN brand app-by-app but names only <brand>-cloud for the siblings —
// the exact shape that made a correctly-issued lux.chat token fail on audience.
const deployedAudiences = "hanzo-app,hanzo-console,hanzo-chat,hanzo-commerce,hanzo-bot," +
	"hanzo-bothub,hanzo-team,hanzo-id,hanzo-cloud,hanzo-dao,hanzo-world,hanzo-platform," +
	"hanzo-insights,lux-cloud,zoo-cloud,pars-cloud,admin-console,hanzo-admin-guard," +
	"hanzo-studio,hanzo-browser,https://api.hanzo.ai"

// TestValidateJWTIssAud_BrandChatToken is the lux.chat outage, frozen. Every brand
// runs the same chat app, so a session token from any of them must authenticate
// against an allowlist that happens to name only this deployment's own brand.
func TestValidateJWTIssAud_BrandChatToken(t *testing.T) {
	t.Setenv("JWT_ISSUER", "https://hanzo.id")
	t.Setenv("WHITELABEL_ISSUERS", "")
	t.Setenv("GATEWAY_ALLOWED_AUDIENCES", deployedAudiences)
	t.Setenv("IAM_AUDIENCE", "hanzo-cloud")

	for _, tc := range []struct{ brand, iss, aud string }{
		{"hanzo", "https://hanzo.id", "hanzo-chat"},
		{"hanzo", "https://hanzo.id", "hanzo-ai"},
		{"lux", "https://lux.id", "lux-chat"},
		{"zoo", "https://zoolabs.id", "zoo-chat"},
		{"pars", "https://pars.id", "pars-chat"},
	} {
		tok := makeJWT(t, map[string]interface{}{"iss": tc.iss, "aud": tc.aud, "owner": tc.brand})
		if err := ValidateJWTIssAud(tok); err != nil {
			t.Errorf("%s.chat token (iss=%s aud=%s) MUST authenticate: %v", tc.brand, tc.iss, tc.aud, err)
		}
	}

	// The mirror follows the app set, not just chat: every app the deployment allows
	// for its own brand is allowed for a sibling brand, including a hyphenated one.
	for _, aud := range []string{"lux-app", "zoo-console", "pars-studio", "lux-admin-guard"} {
		tok := makeJWT(t, map[string]interface{}{"iss": "https://lux.id", "aud": aud})
		if err := ValidateJWTIssAud(tok); err != nil {
			t.Errorf("brand app %q must be mirrored from the allowed hanzo app set: %v", aud, err)
		}
	}
}

// TestWithBrandAudiences_FailSecure holds the line the mirror must not cross: it
// widens the allowlist only along the brand axis, for apps already approved.
func TestWithBrandAudiences_FailSecure(t *testing.T) {
	if got := withBrandAudiences(nil); len(got) != 0 {
		t.Fatalf("an empty allowlist must stay empty (empty => audience not enforced), got %v", got)
	}

	got := withBrandAudiences([]string{"hanzo-chat", "https://api.hanzo.ai", "evil-app"})
	has := func(v string) bool {
		for _, s := range got {
			if s == v {
				return true
			}
		}
		return false
	}
	if !has("lux-chat") {
		t.Errorf("an approved app must mirror onto a trusted brand, got %v", got)
	}
	// An entry naming no known brand is not a brand audience: it is carried through
	// verbatim and never expanded into one.
	for _, never := range []string{"lux-app", "hanzo-evil-app", "lux-evil-app", "lux-https://api.hanzo.ai"} {
		if has(never) {
			t.Errorf("mirror must not invent %q from a non-brand entry, got %v", never, got)
		}
	}
	if !has("evil-app") || !has("https://api.hanzo.ai") {
		t.Errorf("non-brand entries must survive unchanged, got %v", got)
	}
}

// TestBrandNamesCoverBrandIssuers keeps the two brand lists in step. They are stated
// separately because an audience is keyed on the brand (zoo) and an issuer on its host
// (zoolabs.id), so neither derives from the other; adding a brand to one list without
// the other silently half-trusts it.
func TestBrandNamesCoverBrandIssuers(t *testing.T) {
	if len(brandNames) != len(brandIssuerList) {
		t.Fatalf("brandNames %v and brandIssuerList %v must describe the same brands", brandNames, brandIssuerList)
	}
	for _, iss := range brandIssuerList {
		matched := false
		for _, b := range brandNames {
			if strings.Contains(iss, b) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("issuer %q has no brand in brandNames %v — its tokens would pass iss and fail aud", iss, brandNames)
		}
	}
}
