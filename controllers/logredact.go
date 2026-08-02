package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Credential redaction for the error log.
//
// WHY: the API-error line logged the Authorization header verbatim. Measured on
// the live cloud pod, 417 full user JWTs were sitting in the log — real,
// unexpired, replayable bearer tokens for named users. Anyone with log read
// access (o11y, the datastore, a support export) held those users' sessions.
//
// A log line still needs to answer "which caller was this?", so the token is
// replaced by a FINGERPRINT rather than dropped: scheme plus the first 8 bytes of
// its SHA-256. That is enough to correlate repeated failures from one credential
// and to match a report against a session, and it cannot be replayed.
//
// WHY NOT routers.maskKey. That helper already exists for the same-shaped job
// (`apiKey[:6] + "..."`) and reusing it would be the one-way choice, except that
// prefix truncation is actively WRONG for a bearer JWT: every RS256 token begins
// with the same encoded header, so "eyJhbG..." is identical for every user in the
// fleet — it discriminates nothing while still emitting real key material. It
// remains the right tool where it is used, on short opaque `sk-` API keys. Two
// credential shapes, two correct treatments; a single helper here would have to
// branch on shape anyway, and hiding that branch is what makes a leak subtle.

// sensitiveJSONKey matches the JSON keys whose values must never reach a log.
// Passwords are the sharp case: an error on a signup or signin request logs the
// whole body, so a plaintext password would be written to the warehouse — which
// the hashing rule exists to prevent, and a log is not an exception to it.
var sensitiveJSONKey = regexp.MustCompile(
	`(?i)"(password|passwd|pass|secret|token|access_token|refresh_token|id_token|api_key|apikey|client_secret|private_key|otp|code_verifier|card|cvv|cvc|ssn)"\s*:\s*"[^"]*"`)

// sensitiveQueryParam matches credential-bearing query parameters. A key in a URL
// is already the worst place for one, and logging it makes it permanent.
var sensitiveQueryParam = regexp.MustCompile(
	`(?i)\b(access_token|refresh_token|id_token|api_key|apikey|token|secret|client_secret|password|code|otp)=[^&\s]*`)

// redactCredential turns an Authorization header into a correlatable fingerprint.
// The scheme is kept because it is diagnostic (a "Basic" where a "Bearer" was
// expected is a real bug) and carries no secret.
func redactCredential(header string) string {
	h := strings.TrimSpace(header)
	if h == "" {
		return ""
	}
	scheme, rest := "", h
	if i := strings.IndexByte(h, ' '); i > 0 {
		scheme, rest = h[:i], strings.TrimSpace(h[i+1:])
	}
	sum := sha256.Sum256([]byte(rest))
	fp := "sha256:" + hex.EncodeToString(sum[:8])
	if scheme == "" {
		return fp
	}
	return scheme + " " + fp
}

// redactBody removes the values of credential-bearing JSON keys, leaving the rest
// of the body readable — the point of logging a failing body is the shape of the
// request, which survives redaction.
func redactBody(body string) string {
	return sensitiveJSONKey.ReplaceAllStringFunc(body, func(m string) string {
		if i := strings.Index(m, ":"); i > 0 {
			return m[:i+1] + `"[redacted]"`
		}
		return m
	})
}

// redactQuery removes credential values from a raw query string.
func redactQuery(q string) string {
	return sensitiveQueryParam.ReplaceAllStringFunc(q, func(m string) string {
		if i := strings.IndexByte(m, '='); i > 0 {
			return m[:i+1] + "[redacted]"
		}
		return m
	})
}
