// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"fmt"
	"strings"
	"sync"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"

	"github.com/hanzoai/ai/conf"
)

// Establishing the IAM signing cert every bearer token is validated against.
//
// THREE behaviours are possible here and only one of them is right.
//
// It used to WARN AND SERVE. That is a fail-open on the security boundary of the
// whole platform, and it cost a full day: IAM tightened authorization on the
// application read, GetApplication began answering 403, this printed "(auth
// features disabled)" and the process served anyway. With no cert every bearer
// failed to validate, so EVERY authenticated route answered 401 — including free
// ones. The pod stayed Running and passed its probes, so nothing caught it.
//
// It then PANICKED IN AN init(). That fixed the fail-open and bought a worse
// failure: the cert was fetched over the network before any subsystem mounted, so
// a momentary IAM outage was a hard, self-heal-less outage of everything in the
// process. It has now happened twice in production — 2026-07-27 (a one-word
// manifest typo took IAM's pods down) and 2026-08-02 (an IAM build answered no
// /healthz, so its Service held zero endpoints for 25 minutes). Both times the
// blast radius was the entire API, and both times the process could not recover on
// its own after IAM came back.
//
// So: resolve it LAZILY, retry a failure, and fail CLOSED at the request.
// Unresolved is not "no principal" — that is precisely the 401-everything
// fail-open — it is 503 SERVICE UNAVAILABLE at the door (AuthAvailableFilter),
// before any filter reads a token. The security property is unchanged: a request
// is never served with authentication silently switched off. What changes is that
// an IAM blip costs the requests during the blip instead of the process, and the
// first request after IAM returns re-establishes the cert with no restart.
//
// A deployment with no IAM_URL is a legitimate no-auth configuration (dev, tests)
// and resolves cleanly. That is the ONLY path that leaves auth unconfigured.

// retryAfter bounds how often an unresolved cert is re-fetched. Without it, every
// request during an IAM outage becomes an IAM request, so the recovering service
// is met with the full inbound rate the moment it starts answering — the retry
// storm that turns a blip into an outage. One attempt per interval per process is
// enough to recover within a probe cycle.
const retryAfter = 5 * time.Second

var authCfg struct {
	mu       sync.Mutex
	ready    bool      // the cert is established; never re-fetched
	lastErr  error     // why the last attempt failed, for the 503 body
	lastTry  time.Time // when that attempt was, for retryAfter
	attempts int       // attempts since process start, for the log line
}

// AuthReady reports whether this process can validate a bearer token, resolving
// the signing cert on first use and retrying a previous failure at most once per
// retryAfter.
//
// nil means either "the cert is established" or "this deployment runs no IAM".
// A non-nil error means requests that carry authentication MUST be refused with
// 503 — never served, and never treated as anonymous.
func AuthReady() error {
	authCfg.mu.Lock()
	defer authCfg.mu.Unlock()
	if authCfg.ready {
		return nil
	}
	// A failure inside the retry window answers with the SAME error rather than a
	// fresh attempt, so the 503 a caller sees names the real cause for the whole
	// window instead of alternating between causes.
	if !authCfg.lastTry.IsZero() && time.Since(authCfg.lastTry) < retryAfter {
		return authCfg.lastErr
	}
	authCfg.lastTry = time.Now()
	authCfg.attempts++
	if err := resolveAuthConfig(); err != nil {
		authCfg.lastErr = err
		return err
	}
	authCfg.ready = true
	authCfg.lastErr = nil
	return nil
}

// ResetAuthReady drops the resolved state so the next AuthReady re-resolves. It
// exists for tests, which run several configurations in one process.
func ResetAuthReady() {
	authCfg.mu.Lock()
	defer authCfg.mu.Unlock()
	authCfg.ready, authCfg.lastErr, authCfg.lastTry, authCfg.attempts = false, nil, time.Time{}, 0
}

// AuthAttempts reports how many times the cert has been fetched since this process
// started. A test asserts the retry window holds by watching it not move.
func AuthAttempts() int {
	authCfg.mu.Lock()
	defer authCfg.mu.Unlock()
	return authCfg.attempts
}

// resolveAuthConfig performs one attempt: read the application, read its cert, and
// install the cert on the IAM SDK's client. Every failure is returned rather than
// logged-and-swallowed, so the caller decides what it costs.
func resolveAuthConfig() error {
	endpoint := conf.GetConfigString("IAM_URL")
	clientID := conf.GetConfigString("IAM_CLIENT_ID")
	clientSecret := resolveSecretName("IAM_CLIENT_SECRET")
	organization := conf.GetConfigString("IAM_ORG")
	application := conf.GetConfigString("IAM_APP_NAME")

	// No IAM configured: this deployment is deliberately running without auth.
	if endpoint == "" {
		return nil
	}

	iam.InitConfig(endpoint, clientID, clientSecret, "", organization, application)
	app, err := iam.GetApplication(application)
	if err != nil {
		return fmt.Errorf("read IAM application %q from %s: %w", application, endpoint, err)
	}
	if app == nil {
		return fmt.Errorf("IAM application %q does not exist at %s", application, endpoint)
	}

	cert, err := iam.GetCert(app.Cert)
	if err != nil {
		return fmt.Errorf("read cert %q for application %q: %w", app.Cert, application, err)
	}
	if cert == nil {
		return fmt.Errorf("cert %q for application %q does not exist", app.Cert, application)
	}
	if strings.TrimSpace(cert.Certificate) == "" {
		return fmt.Errorf("cert %q for application %q is empty — every bearer would fail to validate",
			app.Cert, application)
	}

	iam.InitConfig(endpoint, clientID, clientSecret, cert.Certificate, organization, application)
	return nil
}
