package controllers

import (
	"regexp"
	"strings"
	"testing"
)

// jwtish matches anything shaped like a JWT reaching a log. The production leak
// was exactly this: 417 of them in the cloud pod's log.
var jwtish = regexp.MustCompile(`ey[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// A real-shaped token: three base64url segments. Not a live credential.
const sampleJWT = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJoYW56by9vbGUiLCJvd25lciI6ImhhbnpvIn0.c2lnbmF0dXJlLXBsYWNlaG9sZGVy"

func TestRedactCredentialKeepsNoKeyMaterial(t *testing.T) {
	got := redactCredential("Bearer " + sampleJWT)
	if jwtish.MatchString(got) {
		t.Fatalf("token survived redaction: %q", got)
	}
	if strings.Contains(got, sampleJWT) {
		t.Fatalf("raw token present: %q", got)
	}
	// The scheme is diagnostic and carries no secret, so it stays.
	if !strings.HasPrefix(got, "Bearer sha256:") {
		t.Fatalf("want a Bearer fingerprint, got %q", got)
	}
}

func TestRedactCredentialIsStableAndDistinguishing(t *testing.T) {
	// Stable: the same credential fingerprints identically, so repeated failures
	// from one caller are still correlatable — the reason to fingerprint rather
	// than drop the field.
	a := redactCredential("Bearer " + sampleJWT)
	if b := redactCredential("Bearer " + sampleJWT); a != b {
		t.Fatalf("not stable: %q vs %q", a, b)
	}
	// Distinguishing: a different credential must not collide.
	if c := redactCredential("Bearer " + sampleJWT + "x"); a == c {
		t.Fatal("two different tokens produced one fingerprint")
	}
}

func TestRedactCredentialHandlesOddInput(t *testing.T) {
	if got := redactCredential(""); got != "" {
		t.Fatalf("empty header should stay empty, got %q", got)
	}
	// A bare token with no scheme still must not leak.
	if got := redactCredential(sampleJWT); strings.Contains(got, sampleJWT) {
		t.Fatalf("schemeless token leaked: %q", got)
	}
	// A wrong scheme is worth seeing in a log — that is a real bug signal — but
	// its credential is not.
	got := redactCredential("Basic dXNlcjpwYXNzd29yZA==")
	if !strings.HasPrefix(got, "Basic sha256:") || strings.Contains(got, "dXNlcjpwYXNz") {
		t.Fatalf("basic creds mishandled: %q", got)
	}
}

func TestRedactBodyDropsPasswordsAndTokens(t *testing.T) {
	body := `{"email":"a@b.com","password":"hunter2","api_key":"sk-live-abc","note":"keep me"}`
	got := redactBody(body)
	for _, secret := range []string{"hunter2", "sk-live-abc"} {
		if strings.Contains(got, secret) {
			t.Fatalf("%q survived: %s", secret, got)
		}
	}
	// The shape of the request is why the body is logged at all, so the
	// non-secret fields must survive.
	for _, keep := range []string{`"email":"a@b.com"`, `"note":"keep me"`} {
		if !strings.Contains(got, keep) {
			t.Fatalf("redaction ate a non-secret field %q: %s", keep, got)
		}
	}
}

func TestRedactQueryDropsCredentialParams(t *testing.T) {
	got := redactQuery("org=all&range=24h&api_key=sk-live-xyz&access_token=" + sampleJWT)
	if jwtish.MatchString(got) || strings.Contains(got, "sk-live-xyz") {
		t.Fatalf("query credential survived: %s", got)
	}
	// The diagnostic params are the reason to log the query.
	if !strings.Contains(got, "org=all") || !strings.Contains(got, "range=24h") {
		t.Fatalf("redaction ate diagnostic params: %s", got)
	}
}

// TestProductionLogLineHasNoJWT reconstructs the exact line that leaked — the
// /v1/ai/usages/cloud error, which fired every ~60s — and asserts no JWT is in
// it. This is the regression that matters: the old code produced this line with
// the full token in the token= field.
func TestProductionLogLineHasNoJWT(t *testing.T) {
	line := "API error: method=GET path=/v1/ai/usages/cloud query=" +
		redactQuery("org=all&range=24h") +
		" token=" + redactCredential("Bearer "+sampleJWT) +
		" body=" + redactBody(`{"password":"hunter2"}`) +
		" response={\"status\":\"error\"}"
	if jwtish.MatchString(line) {
		t.Fatalf("a JWT reached the log line: %s", line)
	}
	if strings.Contains(line, "hunter2") {
		t.Fatalf("a plaintext password reached the log line: %s", line)
	}
}
