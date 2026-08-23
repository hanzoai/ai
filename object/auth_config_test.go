// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"
)

// A deployment that names no IAM is deliberately running without auth, and
// resolves cleanly. It is the ONLY path that leaves authentication unconfigured,
// so it is pinned separately from every failure below.
func TestAuthReady_NoIAMConfigured(t *testing.T) {
	ResetAuthReady()
	t.Setenv("IAM_URL", "")
	if err := AuthReady(); err != nil {
		t.Fatalf("a deployment with no IAM_URL must resolve cleanly, got %v", err)
	}
}

// IAM unreachable does NOT end the process and does NOT resolve. It reports the
// failure, which the edge turns into 503 — never into "no principal", which is the
// 401-everything fail-open this replaced.
func TestAuthReady_UnreachableIAMFailsClosedWithoutPanicking(t *testing.T) {
	ResetAuthReady()
	// A port nothing listens on: the same connection-refused the two production
	// outages produced.
	t.Setenv("IAM_URL", "http://127.0.0.1:1")
	t.Setenv("IAM_APP_NAME", "hanzo-cloud")

	err := AuthReady()
	if err == nil {
		t.Fatal("an unreachable IAM resolved successfully — auth would be silently off")
	}
	if !strings.Contains(err.Error(), "hanzo-cloud") {
		t.Errorf("the failure must name the application it could not read: %v", err)
	}
}

// The retry window holds: a second call inside it reuses the first failure instead
// of dialling again, so an IAM outage does not turn every inbound request into an
// IAM request and meet the recovering service with the full inbound rate.
func TestAuthReady_RetryIsRateLimited(t *testing.T) {
	ResetAuthReady()
	t.Setenv("IAM_URL", "http://127.0.0.1:1")
	t.Setenv("IAM_APP_NAME", "hanzo-cloud")

	first := AuthReady()
	if first == nil {
		t.Fatal("expected a failure")
	}
	got := AuthAttempts()
	for range 50 {
		if err := AuthReady(); err == nil {
			t.Fatal("a failed resolution must keep failing until it succeeds")
		}
	}
	if AuthAttempts() != got {
		t.Fatalf("attempts went %d → %d inside the retry window — 50 requests became %d IAM calls",
			got, AuthAttempts(), AuthAttempts()-got)
	}
}

// A resolvable IAM establishes the cert, and once established it is never fetched
// again — the hot auth path must not carry an IAM round trip per request.
//
// It also proves RECOVERY, which is the whole point of resolving lazily: this
// process began life unable to authenticate anything and needs no restart to start.
func TestAuthReady_ResolvesAndThenStopsFetching(t *testing.T) {
	ResetAuthReady()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		// IAM's routes answer with the record itself. There is no {status, data}
		// envelope to unwrap — a fixture that wrapped one would decode to a
		// zero-valued record, which reads as a successful read of a blank cert.
		//
		// One record is addressed by its key IN THE PATH, so the collection is the
		// prefix and the (owner, name) pair follows it. The default arm below 404s,
		// which is what a stale spelling gets from the real thing.
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/iam/applications/"):
			_, _ = w.Write([]byte(`{"name":"hanzo-cloud","cert":"cert-hanzo"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/iam/certs/"):
			_, _ = w.Write([]byte(`{"name":"cert-hanzo","certificate":"-----BEGIN CERTIFICATE-----\nnot-a-real-cert\n-----END CERTIFICATE-----"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":404,"error":"unexpected path ` + r.URL.Path + `"}`))
		}
	}))
	defer srv.Close()

	// Start from the failure the outages produced, so what is measured is a process
	// that could not authenticate and then could — with no restart in between.
	t.Setenv("IAM_URL", "http://127.0.0.1:1")
	t.Setenv("IAM_APP_NAME", "hanzo-cloud")
	if AuthReady() == nil {
		t.Fatal("expected the unreachable endpoint to fail")
	}

	t.Setenv("IAM_URL", srv.URL)
	// Past the retry window, so the next call really re-attempts.
	waitOutRetryWindow()
	if err := AuthReady(); err != nil {
		t.Fatalf("IAM came back and the process did not recover: %v", err)
	}

	after := calls
	for range 20 {
		if err := AuthReady(); err != nil {
			t.Fatalf("a resolved cert stopped being resolved: %v", err)
		}
	}
	// Past the retry window, because inside it EVERY answer is cached — a resolved
	// cert and an unresolved one are indistinguishable there. Only a call on the far
	// side of the window can tell "established, never fetch again" from "rate-limited
	// for now", and without that this asserted the rate limiter twice and the
	// memoization not at all.
	waitOutRetryWindow()
	if err := AuthReady(); err != nil {
		t.Fatalf("a resolved cert stopped being resolved after the retry window: %v", err)
	}
	if calls != after {
		t.Fatalf("a resolved cert was re-fetched %d time(s) — the hot auth path must carry no IAM round trip", calls-after)
	}
}

// A FAILED ATTEMPT COSTS THE ATTEMPT AND NOTHING ELSE.
//
// The resolution used to reach IAM through the process-wide client, which meant
// installing that client certless first so the reads had somewhere to go. A failure
// between then and the answer left the certless one in place, and from that point
// every bearer token in the process failed to validate — including ones that had
// been validating a moment earlier. An IAM blip is supposed to cost the requests
// during the blip; this made it cost authentication itself, permanently, which is
// the fail-open the whole file is written against.
//
// So it is stated as the property a caller would notice: a credential that verified
// before an attempt still verifies after one that failed.
func TestAuthReady_AFailedAttemptLeavesTheCertAlone(t *testing.T) {
	ResetAuthReady()
	who := iam.User{Owner: "acme", Name: "alice"}
	token := authtest.Token(t, who)
	if _, err := iam.ParseJwtToken(token); err != nil {
		t.Fatalf("the credential did not verify before the attempt: %v", err)
	}

	t.Setenv("IAM_URL", "http://127.0.0.1:1")
	t.Setenv("IAM_APP_NAME", "hanzo-cloud")
	if AuthReady() == nil {
		t.Fatal("expected the unreachable endpoint to fail")
	}

	if _, err := iam.ParseJwtToken(token); err != nil {
		t.Fatalf("a failed attempt threw away the established cert: %v", err)
	}
}

// waitOutRetryWindow sleeps past retryAfter. It is the one place the test suite
// spends real time, so it is named rather than repeated.
func waitOutRetryWindow() { time.Sleep(retryAfter + 50*time.Millisecond) }
