package object

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// This file proves the auth boundary fails CLOSED, against responses the live IAM
// actually emitted.
//
// THE BUG THIS LOCKS OUT. Resolution used to warn and return on any failure to read
// the IAM application or its signing cert. With no cert the process still served,
// but every bearer failed to validate, so every authenticated route answered 401 —
// including free ones like /v1/models. The pod stayed Running and passed its
// probes, so no crashloop, no failed rollout, nothing caught it.
//
// The cure was a panic in an init(), which traded that for an outage of everything
// in the process whenever IAM blinked (twice in production — see auth_config.go).
// Resolution is now lazy and retrying, and the door answers 503. What must NOT
// change, and is what this file holds, is that none of these bodies ever resolves.
//
// WHY THESE FIXTURES ARE REAL. Each body below is the VERBATIM response the live
// IAM emitted, captured 2026-07-27 against the in-cluster service:
//
//	kubectl -n hanzo port-forward svc/iam 18090:80
//	curl -u "hanzo-cloud:$IAM_CLIENT_SECRET" \
//	  'http://127.0.0.1:18090/v1/iam/get-application?id=admin/hanzo-cloud'
//	  -> {"status":403,"error":"forbidden"}
//	curl 'http://127.0.0.1:18090/v1/iam/get-application?id=admin/hanzo-cloud'
//	  -> {"status":401,"error":"authentication required"}
//	curl 'http://127.0.0.1:18090/v1/iam/get-account'
//	  -> {"status":"error","msg":"please sign in first"}
//
// They are recorded ground truth, not our idea of what IAM returns — which matters,
// because our idea of what IAM returns is exactly what was wrong. Note the first two
// carry `status` as a NUMBER while the third carries it as a STRING: IAM serves two
// different envelopes on one surface (the zip framework's error body from the authz
// Guard, and the string-status body from the handler), and the client's
// Response.Status is a string. That type flip is what produced
//
//	json: cannot unmarshal number into Go struct field Response.status of type string
//
// If IAM's envelope is unified later, these fixtures should be re-captured from the
// live service rather than edited to match a new assumption.

// liveIAMBodies are the captured responses, keyed by what produced them.
var liveIAMBodies = map[string]string{
	"authz Guard forbade the read (the outage)": `{"status":403,"error":"forbidden"}`,
	"unauthenticated read":                      `{"status":401,"error":"authentication required"}`,
	"string-status handler error":               `{"status":"error","msg":"please sign in first"}`,
	"empty body":                                ``,
	"HTML error page from a proxy":              `<html><body>502 Bad Gateway</body></html>`,
}

// replay is an iam.HttpClient that answers every request with one fixed body.
type replay struct{ body string }

func (r replay) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, // the envelope, not the transport, carries IAM's verdict
		Body:       io.NopCloser(bytes.NewReader([]byte(r.body))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// TestInitAuthConfigFailsClosed proves that every way the IAM application read can
// fail produces an ERROR rather than a silently unconfigured auth boundary. Before
// the fix each of these returned cleanly and the process served with auth off.
func TestResolveAuthConfigFailsClosed(t *testing.T) {
	t.Setenv("IAM_URL", "http://iam.hanzo.svc")
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "test-secret")
	t.Setenv("IAM_ORG", "hanzo")
	t.Setenv("IAM_APP_NAME", "hanzo-cloud")

	for name, body := range liveIAMBodies {
		t.Run(name, func(t *testing.T) {
			iam.SetHttpClient(replay{body: body})
			t.Cleanup(func() { iam.SetHttpClient(&http.Client{}) })

			ResetAuthReady()
			err := resolveAuthConfig()
			if err == nil {
				t.Fatalf("resolveAuthConfig returned nil for %s (body %q) — "+
					"auth would be silently disabled and every bearer would 401", name, body)
			}
			// The operator has to be able to act on this without a debugger.
			if !strings.Contains(err.Error(), "hanzo-cloud") {
				t.Errorf("error must name the application it could not establish, got: %v", err)
			}
		})
	}
}

// AuthReady REPORTS the failure rather than ending the process, and keeps
// reporting it — so the caller (the door, which answers 503) decides what an
// unreachable identity costs.
//
// This replaces a test that asserted a panic. The panic was the right answer to the
// fail-open and the wrong answer to everything else: it ran in an init(), before any
// subsystem mounted, so a momentary IAM outage killed the whole process and it did
// not come back on its own once IAM did. What must hold is that the failure is never
// swallowed — not that it is fatal.
func TestAuthReadyReportsRatherThanEndingTheProcess(t *testing.T) {
	ResetAuthReady()
	t.Setenv("IAM_URL", "http://iam.hanzo.svc")
	t.Setenv("IAM_APP_NAME", "hanzo-cloud")
	iam.SetHttpClient(replay{body: liveIAMBodies["authz Guard forbade the read (the outage)"]})
	t.Cleanup(func() { iam.SetHttpClient(&http.Client{}) })

	err := AuthReady()
	if err == nil {
		t.Fatal("AuthReady resolved on the outage body — the process would serve with auth off")
	}
	if !strings.Contains(err.Error(), "hanzo-cloud") {
		t.Errorf("the failure must name the application it could not establish, got: %v", err)
	}
	// And it stays unresolved: a later caller must not be told everything is fine
	// because an earlier one already asked.
	if AuthReady() == nil {
		t.Fatal("a second caller was told auth was ready while the cert is still unestablished")
	}
}

// TestInitAuthConfigAllowsNoIAM keeps the legitimate no-auth deployment (dev, tests,
// any build with no IAM_URL) booting. This is the ONLY path that may leave auth
// unconfigured, and it is chosen explicitly by configuration rather than reached by
// swallowing an error.
func TestResolveAuthConfigAllowsNoIAM(t *testing.T) {
	ResetAuthReady()
	t.Setenv("IAM_URL", "")
	if err := resolveAuthConfig(); err != nil {
		t.Fatalf("no IAM_URL must boot cleanly (deliberate no-auth deployment), got: %v", err)
	}
}
