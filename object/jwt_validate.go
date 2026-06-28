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
	iam "github.com/hanzoai/iam"
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

// expectedJWTIssuer returns the trusted issuer: config `jwtIssuer` if set, else
// the first non-empty deployed *_ISSUER env var, else the https://hanzo.id default.
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
		return v
	}
	var out []string
	out = appendUniqueCSV(out, os.Getenv("GATEWAY_ALLOWED_AUDIENCES"))
	out = appendUniqueCSV(out, os.Getenv("IAM_AUDIENCE"))
	out = appendUniqueCSV(out, os.Getenv("AUTH_AUDIENCE"))
	return out
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

// checkIssAud is the pure issuer/audience policy. An empty expectedIss skips the
// issuer check; an empty allowedAud skips the audience check. Fail-secure: when
// an allowlist is set, a token whose audiences match none of it is rejected.
func checkIssAud(iss string, auds []string, expectedIss string, allowedAud []string) error {
	if expectedIss != "" && iss != expectedIss {
		return ErrJWTBadIssuer
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
	return checkIssAud(iss, auds, expectedJWTIssuer(), jwtAudienceAllowlist())
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
