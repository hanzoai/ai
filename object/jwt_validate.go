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
	"errors"
	"os"
	"strings"

	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/ai/internal/iam"
)

// defaultJWTIssuer is the canonical hanzo.id issuer. cloud-api only honors
// access tokens minted by this issuer. Overridable via the `jwtIssuer` app
// config (ConfigMap) — a deployed value, never an env-only knob.
const defaultJWTIssuer = "https://hanzo.id"

var (
	// ErrJWTBadIssuer is returned when a token's iss is not the trusted issuer.
	ErrJWTBadIssuer = errors.New("jwt: untrusted issuer")
	// ErrJWTBadAudience is returned when a token's aud is outside the allowlist.
	ErrJWTBadAudience = errors.New("jwt: audience not allowed")
	// ErrJWTMalformed is returned when the token payload cannot be decoded.
	ErrJWTMalformed = errors.New("jwt: malformed token")
)

// jwtIssuerEnvKeys are the issuer env keys the cloud-api deployment actually sets
// (any one, first non-empty wins). The code previously read only the `jwtIssuer`
// app config, which the deployment never sets — so the issuer policy fell back to
// the default. Reading the deployed keys makes the configured issuer authoritative
// without a rebuild. `jwtIssuer` (app config / ConfigMap) still takes precedence.
var jwtIssuerEnvKeys = []string{"JWT_ISSUER", "IAM_ISSUER", "CLOUD_IAM_ISSUER", "AUTH_ISSUER"}

// expectedJWTIssuer returns the PRIMARY trusted issuer: config `jwtIssuer` if set,
// else the first non-empty deployed *_ISSUER env var, else the https://hanzo.id
// default. This is the deployment's own brand issuer; the FULL trusted set
// (trustedJWTIssuers) also accepts the sibling white-label brand issuers so ONE
// binary validates hanzo AND lux/zoo/pars tokens.
func expectedJWTIssuer() string {
	if v := strings.TrimSpace(conf.GetConfigString("jwtIssuer")); v != "" {
		return v
	}
	for _, k := range jwtIssuerEnvKeys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return defaultJWTIssuer
}

// brandIssuerList is the set of white-label brand OIDC issuers a single cloud
// binary accepts, so a lux token (iss=https://lux.id) validates alongside a hanzo
// token. It mirrors the controllers brandDefs / console BRANDS map (kept in sync
// by hand -- both are the same public HIP-0111 issuer set). Overridable/extendable
// via WHITELABEL_ISSUERS (comma-separated) for a deployment that adds a brand
// without a rebuild.
var brandIssuerList = []string{
	"https://hanzo.id",
	"https://lux.id",
	"https://zoolabs.id",
	"https://pars.id",
}

// brandNames is the set of white-label brands one binary serves. It mirrors
// brandIssuerList, but by BRAND rather than by issuer, because that is what an
// audience is keyed on: HIP-0111 makes client_id == <brand>-<app> == aud. The brand
// cannot be read off the issuer host — zoolabs.id's brand is `zoo` — so the two
// lists are stated separately and kept in step by TestBrandNamesCoverBrandIssuers.
var brandNames = []string{"hanzo", "lux", "zoo", "pars"}

// trustedJWTIssuers returns EVERY issuer the request-auth policy accepts: the
// primary (expectedJWTIssuer, which honors the pinned config/env) UNIONED with the
// white-label brand issuers and any WHITELABEL_ISSUERS override. A token whose
// `iss` is any of these passes the issuer check. The union is fail-secure: it only
// ADDS the known-good brand issuers; it never accepts an arbitrary issuer.
func trustedJWTIssuers() []string {
	out := []string{expectedJWTIssuer()}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		for _, e := range out {
			if e == v {
				return
			}
		}
		out = append(out, v)
	}
	for _, iss := range brandIssuerList {
		add(iss)
	}
	for _, iss := range splitCSV(os.Getenv("WHITELABEL_ISSUERS")) {
		add(iss)
	}
	return out
}

// jwtAudienceAllowlist returns the accepted `aud` client-ids. It mirrors the
// gateway's audience allowlist (iamauth.AudiencesFromEnv): GATEWAY_ALLOWED_AUDIENCES
// (comma-separated) is the primary source, with the cloud-api IAM_AUDIENCE and the
// legacy AUTH_AUDIENCE single values folded IN (widen, never narrow). The
// `jwtAudiences` app config still takes precedence so the allowlist can be pinned
// via ConfigMap. The bug this closes: the deployment set
// GATEWAY_ALLOWED_AUDIENCES/IAM_AUDIENCE but the code read only `jwtAudiences`, so
// the audience check was silently disabled (issuer-only) on the direct ingress.
//
// Empty result => audience is NOT enforced (issuer-only). Live the deployment
// always sets GATEWAY_ALLOWED_AUDIENCES, so the check is enforced.
func jwtAudienceAllowlist() []string {
	if v := splitCSV(conf.GetConfigString("jwtAudiences")); len(v) > 0 {
		// A pinned allowlist is mirrored across the brands too — one binary must accept
		// every brand's bearer for an app it already allows (fail-secure: only ADDS).
		return withBrandAudiences(v)
	}
	var out []string
	out = appendUniqueCSV(out, os.Getenv("GATEWAY_ALLOWED_AUDIENCES"))
	out = appendUniqueCSV(out, os.Getenv("IAM_AUDIENCE"))
	out = appendUniqueCSV(out, os.Getenv("AUTH_AUDIENCE"))
	// White-label: mirror a NON-EMPTY allowlist's app set across every brand, so a
	// lux/zoo/pars bearer validates even when the deployed GATEWAY_ALLOWED_AUDIENCES
	// names only this deployment's own brand. Fail-secure — an app is mirrored only
	// onto brands already trusted as issuers. An EMPTY allowlist is left empty so
	// the "no allowlist => audience not enforced (issuer-only)" contract is
	// preserved (dev/test with no audience env stays issuer-only, not suddenly
	// enforcing on the brand set); live always sets GATEWAY_ALLOWED_AUDIENCES.
	return withBrandAudiences(out)
}

// withBrandAudiences mirrors the allowlist's APP set across every white-label brand.
// The deployment enumerates the apps once, for its own brand (hanzo-app, hanzo-chat,
// hanzo-console, …). A sibling brand runs the SAME apps under the SAME names, and
// HIP-0111 makes the app name the audience — so for every <brand>-<app> already
// allowed, every other brand's <app> is allowed too. One list to maintain, and it is
// the one the deployment already maintains.
//
// This is the predicate that took lux.chat down. The fold used to add only
// <brand>-cloud, so a lux.chat session token (aud=lux-chat) — correctly issued,
// correctly signed, issuer already trusted — was refused on audience alone while the
// byte-identical hanzo.chat case passed, because the deployed allowlist happened to
// name hanzo-chat and no other brand's chat.
//
// Fail-secure. It only ever mirrors an app the deployment ALREADY approved onto a
// brand this binary ALREADY trusts the issuer of; an unrecognized entry (a bare host
// like https://api.hanzo.ai, or an app under an unknown brand) is passed through
// untouched and never expanded. An empty allowlist is returned unchanged, preserving
// the "empty => audience not enforced" contract.
func withBrandAudiences(list []string) []string {
	if len(list) == 0 {
		return list
	}
	out := make([]string, len(list))
	copy(out, list)
	for _, entry := range list {
		brand, app, ok := splitBrandApp(entry)
		if !ok {
			continue
		}
		for _, b := range brandNames {
			if b != brand {
				out = appendUniqueCSV(out, b+"-"+app)
			}
		}
	}
	return out
}

// splitBrandApp splits a <brand>-<app> audience whose brand is one this binary
// serves. The FIRST hyphen divides them, so a multi-word app survives the round trip
// (hanzo-admin-guard => brand hanzo, app admin-guard) and mirrors as lux-admin-guard.
// An audience that names no known brand is not a brand audience: ok is false.
func splitBrandApp(aud string) (brand, app string, ok bool) {
	for _, b := range brandNames {
		if rest, found := strings.CutPrefix(aud, b+"-"); found && rest != "" {
			return b, rest, true
		}
	}
	return "", "", false
}

// appendUniqueCSV splits a comma-separated raw value and appends each non-empty,
// not-already-present entry to out (preserving order). This is the audience-merge
// primitive: GATEWAY_ALLOWED_AUDIENCES widened by IAM_AUDIENCE/AUTH_AUDIENCE.
func appendUniqueCSV(out []string, raw string) []string {
	for _, a := range splitCSV(raw) {
		seen := false
		for _, e := range out {
			if e == a {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, a)
		}
	}
	return out
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, a := range strings.Split(raw, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// jwtUnverifiedClaims decodes (WITHOUT verifying the signature) the iss/aud
// registered claims from a JWT payload segment. Signature verification is
// iam.ParseJwtToken's job; this only extracts the registered claims for policy
// checks. It decodes the raw payload to a map to sidestep the iam.Claims
// embedded-field JSON-tag collision — both User.Iss and RegisteredClaims.Issuer
// map to "iss", so the parsed struct leaves BOTH empty and cannot be trusted
// for iss/aud. The raw map is authoritative.
func jwtUnverifiedClaims(token string) (iss string, auds []string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", nil, ErrJWTMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", nil, ErrJWTMalformed
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return "", nil, ErrJWTMalformed
	}
	if raw, ok := m["iss"]; ok {
		_ = json.Unmarshal(raw, &iss)
	}
	if raw, ok := m["aud"]; ok {
		auds = decodeAudience(raw)
	}
	return iss, auds, nil
}

// decodeAudience handles the JWT `aud` claim, which per RFC 7519 may be either a
// single string or an array of strings.
func decodeAudience(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var arr []string
	_ = json.Unmarshal(raw, &arr)
	return arr
}

// checkIssAud is the pure issuer/audience policy. An empty expectedIss set skips
// the issuer check; an empty allowedAud skips the audience check. Fail-secure:
// when a set is provided, a token whose iss/aud matches none of it is rejected.
// expectedIss is a SET so ONE binary trusts every white-label brand issuer.
func checkIssAud(iss string, auds []string, expectedIss []string, allowedAud []string) error {
	if len(expectedIss) > 0 {
		ok := false
		for _, want := range expectedIss {
			if want != "" && iss == want {
				ok = true
				break
			}
		}
		if !ok {
			return ErrJWTBadIssuer
		}
	}
	if len(allowedAud) > 0 {
		for _, a := range auds {
			for _, allow := range allowedAud {
				if a == allow {
					return nil
				}
			}
		}
		return ErrJWTBadAudience
	}
	return nil
}

// ValidateJWTIssAud enforces the cloud-api issuer/audience policy on a token
// string. Call AFTER the signature has been verified (the claims are read from
// the raw payload, so a forged unsigned token would still be caught here on
// iss/aud, but signature verification remains mandatory upstream).
func ValidateJWTIssAud(token string) error {
	iss, auds, err := jwtUnverifiedClaims(token)
	if err != nil {
		return err
	}
	return checkIssAud(iss, auds, trustedJWTIssuers(), jwtAudienceAllowlist())
}

// TokenIsOwnBrand reports whether a JWT was issued by THIS deployment's OWN
// primary brand issuer (expectedJWTIssuer) — as opposed to a sibling white-label
// brand issuer that trustedJWTIssuers ALSO accepts for sign-in. The iss is read
// from the raw payload (the same source ValidateJWTIssAud trusts); call only on a
// token already accepted by ParseAndValidateJWT (signature + issuer allowlist),
// so this only DISTINGUISHES which trusted brand signed it, never grants trust.
//
// This is the primitive that keeps cross-tenant / all-customer financial
// disclosure bound to the deployment's OWN super admins: a grant gated on it
// stays shut for a sibling brand's admin token even if a future IAM SDK starts
// verifying every brand's signing cert (today one binary trusts many brand
// ISSUERS for auth, but only verifies the primary brand's SIGNATURE). Fail-secure:
// a malformed token or one whose iss is empty is NOT own-brand.
func TokenIsOwnBrand(token string) bool {
	iss, _, err := jwtUnverifiedClaims(token)
	if err != nil || iss == "" {
		return false
	}
	return iss == expectedJWTIssuer()
}

// ParseAndValidateJWT verifies the token signature via IAM AND enforces the
// cloud-api issuer/audience policy. This is the ONE JWT entry point for
// cloud-api request auth — handlers and filters must never call
// iam.ParseJwtToken directly, so iss/aud are checked on every JWT auth path.
func ParseAndValidateJWT(token string) (*iam.Claims, error) {
	claims, err := iam.ParseJwtToken(token)
	if err != nil {
		return nil, err
	}
	if err := ValidateJWTIssAud(token); err != nil {
		return nil, err
	}
	return claims, nil
}
