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
// WHAT THESE FIXTURES ARE. Each is a way the application read fails against IAM's
// noun surface:
//
//	curl -u "hanzo-cloud:$IAM_CLIENT_SECRET" \
//	  'http://127.0.0.1:18090/v1/iam/applications/get?owner=admin&name=hanzo-cloud'
//	  -> 403 {"status":403,"error":"forbidden"}
//	curl 'http://127.0.0.1:18090/v1/iam/applications/get?owner=admin&name=hanzo-cloud'
//	  -> 401 {"status":401,"error":"authentication required"}
//	curl 'http://127.0.0.1:18090/v1/iam/get-application?id=admin/hanzo-cloud'
//	  -> 404 {"status":404,"error":"Cannot GET /v1/iam/get-application"}
//
// THE STATUS IS NOW THE VERDICT. The retired verb surface reported its outcome
// inside a {status, msg} envelope and answered HTTP 200 whatever happened, so the
// client read the envelope and ignored the transport. The noun routes refuse with
// the real code, which is why each fixture carries one — and why the last is here
// at all: a request built the old way is not forbidden or unauthenticated, it
// addresses nothing, and 404 is a shape this boundary had never had to survive.
//
// A body is still checked alongside the status, because the 200s are the dangerous
// ones. An error envelope decoded as a record yields a zero-valued one, and a blank
// cert that arrives without complaint is precisely the fail-open this file exists to
// prevent.

// failure is one way the read can fail: what IAM answered, and with which status.
type failure struct {
	status int
	body   string
}

// liveIAMBodies are those responses, keyed by what produced them.
var liveIAMBodies = map[string]failure{
	"authz Guard forbade the read (the outage)": {403, `{"status":403,"error":"forbidden"}`},
	"unauthenticated read":                      {401, `{"status":401,"error":"authentication required"}`},
	"a request built the retired way":           {404, `{"status":404,"error":"Cannot GET /v1/iam/get-application"}`},
	"empty body":                                {200, ``},
	"HTML error page from a proxy":              {200, `<html><body>502 Bad Gateway</body></html>`},
	"an error envelope served as a record":      {200, `{"status":"error","msg":"please sign in first"}`},
}

// replay is an iam.HttpClient that answers every request with one fixed response.
type replay struct{ answer failure }

func (r replay) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: r.answer.status,
		Body:       io.NopCloser(bytes.NewReader([]byte(r.answer.body))),
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

	for name, answer := range liveIAMBodies {
		t.Run(name, func(t *testing.T) {
			iam.SetHttpClient(replay{answer: answer})
			t.Cleanup(func() { iam.SetHttpClient(&http.Client{}) })

			ResetAuthReady()
			err := resolveAuthConfig()
			if err == nil {
				t.Fatalf("resolveAuthConfig returned nil for %s (%d %q) — "+
					"auth would be silently disabled and every bearer would 401",
					name, answer.status, answer.body)
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
	iam.SetHttpClient(replay{answer: liveIAMBodies["authz Guard forbade the read (the outage)"]})
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
