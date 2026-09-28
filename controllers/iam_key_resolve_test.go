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

package controllers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGetUserByAccessKeyUsesClientSecretBasic pins the ONE transport sk- key
// resolution may use.
//
// IAM gates get-user?accessKey= on an authenticated confidential-APP principal
// (authz.CapKeyResolve requires p.App != ""), and it derives p.App ONLY from
// client_secret_basic. Both other spellings have shipped to production and both
// were dead ends:
//
//   - clientId/clientSecret as QUERY PARAMS  → 401 "authentication required"
//     (no principal at all — and a credential in a URL, which lands in logs)
//   - a client_credentials BEARER            → 200 {"status":"error",
//     "msg":"auth:Unauthorized operation"} (a machine token has no user row, so
//     the principal is org-scoped with an EMPTY App and holds the capability
//     vacuously — which the handler refuses)
//
// So this test asserts the positive (Basic, matching the configured credential)
// AND the negative (no credential anywhere in the URL).
func TestGetUserByAccessKeyUsesClientSecretBasic(t *testing.T) {
	const (
		clientID     = "hanzo-cloud"
		clientSecret = "s3cr3t"
		accessKey    = "sk-test-000000000000000000000000000000ab"
	)

	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()

		id, secret, ok := r.BasicAuth()
		if !ok {
			t.Errorf("no Basic credential presented; Authorization=%q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if id != clientID || secret != clientSecret {
			t.Errorf("Basic credential = %q:<secret>, want %q", id, clientID)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","msg":"","data":{"owner":"hanzo","name":"test-api-user"}}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", clientID)
	t.Setenv("IAM_CLIENT_SECRET", clientSecret)

	u, err := GetUserByAccessKey(accessKey)
	if err != nil {
		t.Fatalf("GetUserByAccessKey: %v", err)
	}
	if u == nil || u.Owner != "hanzo" || u.Name != "test-api-user" {
		t.Fatalf("resolved user = %+v, want hanzo/test-api-user", u)
	}

	if got := gotQuery.Get("accessKey"); got != accessKey {
		t.Errorf("accessKey query = %q, want %q", got, accessKey)
	}
	// No credential may ride in the URL — that is the shape IAM refuses AND a
	// secret in an access log.
	for _, k := range []string{"clientId", "clientSecret", "client_id", "client_secret"} {
		if _, ok := gotQuery[k]; ok {
			t.Errorf("credential %q leaked into the request URL", k)
		}
	}
}

// TestGetUserByAccessKeyRefusalIsAnError asserts a denied resolution surfaces as
// an ERROR, never a nil user with a nil error. IAM answers a refusal inside a 200
// envelope ({"status":"error"}), so a caller that branched on the HTTP code alone
// would read the denial as "no such key" and fail open.
func TestGetUserByAccessKeyRefusalIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"error","msg":"auth:Unauthorized operation","data":null}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	if _, err := GetUserByAccessKey("sk-test-000000000000000000000000000000ab"); err == nil {
		t.Fatal("a refused resolution must be an error, got nil")
	}
}

// TestGetUserByAccessKeyRelaysReasonOnNon200 pins the distinction that cost a
// day of triage: IAM answers an absent key with HTTP 400 AND a body naming
// `key_unknown`. Reading only the status line turns "this key is not there" into
// "IAM returned status 400", which reads like key validation is broken and sends
// the search to the wrong layer. The reason is in the body on non-200 too.
func TestGetUserByAccessKeyRelaysReasonOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","code":"key_unknown","msg":"the entity does not exist","data":null}`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")

	_, err := GetUserByAccessKey("sk-test-000000000000000000000000000000ab")
	if err == nil {
		t.Fatal("an unknown key must be an error, got nil")
	}
	// The holder must be told what to DO. keyRefusal turns key_unknown into the one
	// cure that fits every case behind that code — mint a new key — and a bare status
	// number tells them nothing. The sentence deliberately does not guess whether the
	// key was revoked, replaced, or never resolved at all; see keyRefusal.
	if !strings.Contains(err.Error(), "does not resolve") || !strings.Contains(err.Error(), "mint a new one") {
		t.Errorf("non-200 dropped IAM's reason — want the key_unknown refusal, got: %v", err)
	}
	if strings.Contains(err.Error(), "returned status") {
		t.Errorf("relayed the status line instead of the named reason: %v", err)
	}
}

// TestGetUserByAccessKeyRequiresCredentials asserts the resolver fails CLOSED
// when no confidential-client credential is configured: it must not fall back to
// an unauthenticated call that IAM would 401 anyway.
func TestGetUserByAccessKeyRequiresCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("resolver called IAM with no configured credential")
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "")
	t.Setenv("IAM_CLIENT_SECRET", "")
	// iamClientCreds falls back to KMS when the env is empty; an environment that
	// HAS a KMS-backed credential is not the case under test.
	if id, secret := iamClientCreds(); id != "" || secret != "" {
		t.Skip("a client credential is configured out-of-band; nothing to fail closed on")
	}

	if _, err := GetUserByAccessKey("sk-test-000000000000000000000000000000ab"); err == nil {
		t.Fatal("missing client credentials must be an error, got nil")
	}
}

// IAM is asked about a key once a minute, whatever asks: the balance gate, the authz
// filter and the controller each resolve the key on every request, and IAM takes
// hundreds of milliseconds an ask. What IAM says about the key is held, a principal
// or a refusal; a fault in reaching it is asked again; each caller gets its own user.
func TestKeyAnswerIsHeldForAMinute(t *testing.T) {
	var asks atomic.Int64
	answer := `{"status":"ok","msg":"","data":{"owner":"hanzo","name":"held"}}`
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		body := answer
		mu.Unlock()
		if body == "" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")
	set := func(b string) { mu.Lock(); answer = b; mu.Unlock() }

	const key = "sk-held-00000000000000000000000000000000"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if u, err := GetUserByAccessKey(key); err != nil || u == nil || u.Name != "held" {
				t.Errorf("resolved %+v, %v", u, err)
			}
		}()
	}
	wg.Wait()
	a, err := GetUserByAccessKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if n := asks.Load(); n != 1 {
		t.Fatalf("IAM asked %d times for one key, want 1", n)
	}
	a.Name = "changed"
	if b, _ := GetUserByAccessKey(key); b.Name != "held" {
		t.Fatalf("a caller's change reached another caller: %q", b.Name)
	}

	// A refusal of the key is held as the principal is.
	const gone = "sk-gone-00000000000000000000000000000000"
	set(`{"status":"error","msg":"the entity does not exist","code":"key_unknown","data":null}`)
	for i := 0; i < 2; i++ {
		if _, err := GetUserByAccessKey(gone); err == nil || !strings.Contains(err.Error(), "does not resolve") {
			t.Fatalf("refusal = %v", err)
		}
	}
	if n := asks.Load(); n != 2 {
		t.Fatalf("IAM asked %d times, want 2: the refusal is held", n)
	}

	// A fault is not an answer about the key: it is asked again.
	const flaky = "sk-flaky-0000000000000000000000000000000"
	set("")
	for i := 0; i < 2; i++ {
		if _, err := GetUserByAccessKey(flaky); err == nil {
			t.Fatal("a 502 must not resolve")
		}
	}
	if n := asks.Load(); n != 4 {
		t.Fatalf("IAM asked %d times, want 4: a fault is asked again", n)
	}

	// Past the minute IAM is asked again, so a revoked key stops resolving.
	prev := keyTTL
	keyTTL = 0
	defer func() { keyTTL = prev }()
	set(`{"status":"ok","msg":"","data":{"owner":"hanzo","name":"fresh"}}`)
	const later = "sk-later-0000000000000000000000000000000"
	_, _ = GetUserByAccessKey(later)
	set(`{"status":"error","msg":"the entity does not exist","code":"key_unknown","data":null}`)
	if _, err := GetUserByAccessKey(later); err == nil {
		t.Fatal("a key revoked after its answer expired must not resolve")
	}
}
