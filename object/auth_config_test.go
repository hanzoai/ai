// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
// failure, which the door turns into 503 — never into "no principal", which is the
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
		switch {
		case strings.Contains(r.URL.Path, "get-application"):
			_, _ = w.Write([]byte(`{"status":"ok","data":{"name":"hanzo-cloud","cert":"cert-hanzo"}}`))
		case strings.Contains(r.URL.Path, "get-cert"):
			_, _ = w.Write([]byte(`{"status":"ok","data":{"name":"cert-hanzo","certificate":"-----BEGIN CERTIFICATE-----\nnot-a-real-cert\n-----END CERTIFICATE-----"}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"error","msg":"unexpected path ` + r.URL.Path + `"}`))
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

// waitOutRetryWindow sleeps past retryAfter. It is the one place the test suite
// spends real time, so it is named rather than repeated.
func waitOutRetryWindow() { time.Sleep(retryAfter + 50*time.Millisecond) }
