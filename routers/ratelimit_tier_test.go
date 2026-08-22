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

package routers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/ai/object"
)

// A tier is a claim about what someone pays for, and the rate limiter turns it
// into how fast they may go. So the two transports that can carry it have to
// agree on one answer, and the cheap correct one has to win.
//
// The cloud edge answers /v1/billing/tier for a signed-in payer. A service token
// names no payer, so in a co-resident deployment that route can only say "sign
// in" — and a caller who pays for the top plan would be shaped like the bottom
// one. The host installs object.TierReader precisely so the question has an
// answer; when it is there, the edge must not be reached at all.
func TestTierIsReadFromTheReaderRatherThanTheEdge(t *testing.T) {
	var edgeCalls int32
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&edgeCalls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer edge.Close()

	var askedSubject, askedNamespace string
	object.SetTierReader(func(_ context.Context, subject, namespace string) (string, error) {
		askedSubject, askedNamespace = subject, namespace
		return "pro", nil
	})
	defer object.SetTierReader(nil)

	tc := &TierCache{endpoint: edge.URL, token: "opaque-service-token", client: edge.Client()}
	got, err := tc.commerceTierLookup("acme")
	if err != nil {
		t.Fatalf("lookup through the reader failed: %v", err)
	}
	if got != TierZenPro {
		t.Errorf("tier = %q, want %q — the reader said the plan is \"pro\"", got, TierZenPro)
	}
	if n := atomic.LoadInt32(&edgeCalls); n != 0 {
		t.Errorf("the edge was called %d time(s); with a reader installed it must not be reached, "+
			"or the answer depends on a credential the caller does not have", n)
	}
	// An org slug is its own tenant. If these ever differ the reader is being asked
	// about one account inside another's books.
	if askedSubject != "acme" || askedNamespace != "acme" {
		t.Errorf("reader asked subject=%q namespace=%q, want both \"acme\"", askedSubject, askedNamespace)
	}
}

// A standalone ai has no host and therefore no reader, and it still has to work.
// The HTTP route is what it has instead of one — not a leftover.
func TestTierFallsBackToTheEdgeWithoutAReader(t *testing.T) {
	object.SetTierReader(nil)

	var sawUser string
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUser = r.URL.Query().Get("user")
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"tier":{"name":"enterprise"}}`)
	}))
	defer edge.Close()

	tc := &TierCache{endpoint: edge.URL, client: edge.Client()}
	got, err := tc.commerceTierLookup("acme")
	if err != nil {
		t.Fatalf("standalone lookup failed: %v", err)
	}
	if got != TierZenEnterprise {
		t.Errorf("tier = %q, want %q", got, TierZenEnterprise)
	}
	if sawUser != "acme" {
		t.Errorf("edge asked about user=%q, want \"acme\"", sawUser)
	}
}

// Both transports name a tier the same way, or the same customer is two speeds
// depending on how the binary happens to be deployed.
func TestBothTransportsNameATierTheSameWay(t *testing.T) {
	for _, plan := range []string{"free", "pro", "team", "enterprise", "custom", "starter", "scale", "nonsense"} {
		object.SetTierReader(func(_ context.Context, _, _ string) (string, error) { return plan, nil })
		native, err := (&TierCache{}).commerceTierLookup("acme")
		object.SetTierReader(nil)
		if err != nil {
			t.Fatalf("plan %q through the reader: %v", plan, err)
		}

		edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, `{"tier":{"name":"`+plan+`"}}`)
		}))
		overWire, err := (&TierCache{endpoint: edge.URL, client: edge.Client()}).commerceTierLookup("acme")
		edge.Close()
		if err != nil {
			t.Fatalf("plan %q over HTTP: %v", plan, err)
		}

		if native != overWire {
			t.Errorf("plan %q reads as %q through the reader and %q over HTTP", plan, native, overWire)
		}
	}
}

// With neither a reader nor an endpoint there is nothing to ask. Saying so beats
// building a URL against an empty host and reporting whatever that does.
func TestNoReaderAndNoEndpointSaysSo(t *testing.T) {
	object.SetTierReader(nil)
	if _, err := (&TierCache{}).commerceTierLookup("acme"); err == nil {
		t.Fatal("expected an error when nothing can answer, got nil")
	}
}
