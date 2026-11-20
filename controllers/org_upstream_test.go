// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// The org is said to the upstreams Hanzo runs, by hanzo-inc/zen's rule for the same
// header, and to nobody else.
func TestOnlyAnUpstreamHanzoRunsIsToldTheOrg(t *testing.T) {
	for base, want := range map[string]bool{
		"http://zen.zen.svc.cluster.local:8080":            true, // ZEN_URL in production
		"http://enso.enso.svc.cluster.local:8080":          true, // ENSO_URL in production
		"http://hanzo-router.hanzo.svc.cluster.local:1235": true,
		"http://hanzo-router.hanzo.svc:1235/v1":            true,
		"plugin://zen":                                     true, // a family linked into this process
		"http://10.0.0.19:1235/v1":                         true,
		"http://127.0.0.1:36900/v1":                        true,
		"http://[::1]:8080/v1":                             true,
		"http://localhost:1234/v1":                         true,
		"http://spark.local:30000/v1":                      true,
		"https://engine.hanzo.ai/v1":                       true,
		"https://openrouter.ai/api":                        false, // OPENROUTER_URL in production
		"https://openrouter.ai/api/v1":                     false,
		"https://api.anthropic.com":                        false,
		"https://api.openai.com":                           false,
		"https://inference.do-ai.run/v1":                   false,
		"https://hanzo.ai.attacker.example/v1":             false,
		"https://nothanzo.ai/v1":                           false,
		"http://8.8.8.8/v1":                                false,
		"":                                                 false,
		"::not a url":                                      false,
	} {
		if got := firstParty(base); got != want {
			t.Errorf("firstParty(%q) = %v, want %v", base, got, want)
		}
	}
}

// nameOrg replaces whatever the header carried: an org meant for one family is
// taken off before the request goes to a vendor's.
func TestNameOrgClearsTheOrgForAVendor(t *testing.T) {
	h := http.Header{}
	nameOrg(h, "http://zen.zen.svc.cluster.local:8080", "acme")
	if got := h.Get(orgHeader); got != "acme" {
		t.Fatalf("a Hanzo family was told %q, want acme", got)
	}
	nameOrg(h, "https://openrouter.ai/api", "acme")
	if _, ok := h[http.CanonicalHeaderKey(orgHeader)]; ok {
		t.Fatalf("a vendor was told %q", h.Get(orgHeader))
	}
	nameOrg(h, "http://zen.zen.svc.cluster.local:8080", "")
	if _, ok := h[http.CanonicalHeaderKey(orgHeader)]; ok {
		t.Fatal("a call for no org named one")
	}
}

// The OpenRouter family is a vendor's API: a request it serves reaches openrouter.ai
// without the caller's org. The same family at an address Hanzo runs (a mirror, a
// test double) is told the org, which is what shows the check reads the header the
// vendor would have received.
func TestTheOpenRouterFamilyIsNeverToldTheOrg(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string // OPENROUTER_URL; "" keeps the test vendor's own loopback address
		want string
	}{
		{"at openrouter.ai", "http://openrouter.ai", ""},
		{"at an address Hanzo runs", "", "acme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vendor := withAliasVendor(t)
			addr := strings.TrimPrefix(os.Getenv(openrouterFam.urlKey), "http://")
			if tc.base != "" {
				t.Setenv(openrouterFam.urlKey, tc.base)
			}
			heard := hearOrgs(t, addr)

			body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"2+2?"}]}`)
			c := visit(http.MethodPost, "/v1/chat/completions")
			c.Fiber().Request().SetBody(body)
			alice := &iam.User{Owner: "acme", Name: "alice"}
			if refused := c.pipeToFamily(openrouterFam, "chat/completions", "openai", "gpt-4o", body, false, 0, "acme", alice, false, nil, time.Now()); refused != nil {
				t.Fatalf("the family refused the request: %+v", refused)
			}
			if asked := vendor.models(); len(asked) != 1 {
				t.Fatalf("the vendor was asked %d times (%v), want once", len(asked), asked)
			}
			got := heard()
			if len(got) != 1 {
				t.Fatalf("%d requests left ai, want 1", len(got))
			}
			if got[0] != tc.want {
				t.Fatalf("the family %s was told org %q, want %q", tc.name, got[0], tc.want)
			}
			routedFor(t, "acme")
		})
	}
}

// hearOrgs sends every family request to addr, whatever host its URL names, and
// records the org header each one carried as it left ai.
func hearOrgs(t *testing.T, addr string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var orgs []string
	dial := &net.Dialer{}
	next := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dial.DialContext(ctx, network, addr)
	}}
	prev := zenPipeClient
	zenPipeClient = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		orgs = append(orgs, r.Header.Get(orgHeader))
		mu.Unlock()
		return next.RoundTrip(r)
	})}
	t.Cleanup(func() {
		zenPipeClient = prev
		next.CloseIdleConnections()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), orgs...)
	}
}

// routedFor waits for the routing row a served family call files off the request
// path (recordFamilyRouting), so it is not still writing when the test's store goes.
func routedFor(t *testing.T, org string) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if rows, err := object.GetRoutingEvents(org, ""); err == nil && len(rows) > 0 {
			return
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
