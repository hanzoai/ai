// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
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

// The key a vendor's cache is routed by names the call's partition and nothing a
// caller can choose for another: the same org with the same key routes together,
// two orgs never do, and a key one org copies from another's traffic is still its own.
func TestACacheKeyNamesThePartitionAndNoOther(t *testing.T) {
	body := func(key string) []byte {
		if key == "" {
			return []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
		}
		return []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":` + strconv.Quote(key) + `}`)
	}
	key := func(part, asked string) string {
		var m struct {
			Key string `json:"prompt_cache_key"`
		}
		if err := json.Unmarshal(withCacheKey(body(asked), part), &m); err != nil {
			t.Fatal(err)
		}
		return m.Key
	}
	acme := key("acme", "")
	if !strings.HasPrefix(acme, cacheKeyPrefix) || len(acme) != len(cacheKeyPrefix)+32 || strings.Contains(acme, "acme") {
		t.Fatalf("acme's key is %q, want hz- and 32 hex digits naming no org", acme)
	}
	if acme != key("acme", "") {
		t.Fatal("one org's calls are not routed together")
	}
	if acme == key("evil", "") {
		t.Fatal("two orgs share a cache key")
	}
	if key("evil", acme) == acme || key("evil", "acme") == acme {
		t.Fatal("a key one org copied names another org's partition")
	}
	if key("acme", "thread-1") == key("acme", "thread-2") || key("acme", "thread-1") == acme {
		t.Fatal("a caller's own key no longer splits its own traffic")
	}
	alice, bob := partition("hanzo", "hanzo/alice"), partition("hanzo", "hanzo/bob")
	if alice == bob || key(alice, "") == key(bob, "") {
		t.Fatal("two wallets in the signup org share a partition")
	}
	if partition("acme", "acme") != "acme" || partition("acme", "") != "acme" || partition("", "x") != "" {
		t.Fatal("an org that pays for itself is not its own partition")
	}
	for _, b := range [][]byte{body("thread-1"), []byte("not json")} {
		if got := withCacheKey(b, ""); string(got) != string(b) {
			t.Fatalf("a call with no partition was rewritten: %s", got)
		}
	}
	if got := withCacheKey([]byte("not json"), "acme"); string(got) != "not json" {
		t.Fatalf("a body that is not JSON was rewritten: %s", got)
	}
}

// A chat the OpenRouter family sends to openrouter.ai carries its org's cache key,
// different for two orgs that send the same body. The same family at an address
// Hanzo runs is sent the caller's own key as written.
func TestAVendorsCacheIsRoutedPerOrg(t *testing.T) {
	ask := func(t *testing.T, base string, orgs ...string) []string {
		t.Helper()
		vendor := withAliasVendor(t)
		addr := strings.TrimPrefix(os.Getenv(openrouterFam.urlKey), "http://")
		if base != "" {
			t.Setenv(openrouterFam.urlKey, base)
		}
		hearOrgs(t, addr)
		body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"2+2?"}],"prompt_cache_key":"thread-1"}`)
		for _, org := range orgs {
			c := visit(http.MethodPost, "/v1/chat/completions")
			c.Fiber().Request().SetBody(body)
			u := &iam.User{Owner: org, Name: "alice"}
			if refused := c.pipeToFamily(openrouterFam, "chat/completions", "openai", "gpt-4o", body, false, 0, org, u, false, nil, time.Now()); refused != nil {
				t.Fatalf("the family refused %s: %+v", org, refused)
			}
			routedFor(t, org)
		}
		vendor.mu.Lock()
		defer vendor.mu.Unlock()
		keys := make([]string, 0, len(vendor.bodies))
		for _, b := range vendor.bodies {
			var m struct {
				Key string `json:"prompt_cache_key"`
			}
			if err := json.Unmarshal([]byte(b), &m); err != nil {
				t.Fatalf("the vendor got %s: %v", b, err)
			}
			keys = append(keys, m.Key)
		}
		return keys
	}
	t.Run("at openrouter.ai", func(t *testing.T) {
		keys := ask(t, "http://openrouter.ai", "acme", "evil", "acme")
		if len(keys) != 3 {
			t.Fatalf("the vendor heard %d calls, want 3", len(keys))
		}
		if !strings.HasPrefix(keys[0], cacheKeyPrefix) || keys[0] == "thread-1" {
			t.Fatalf("acme's call reached the vendor keyed %q, want its partition's key", keys[0])
		}
		if keys[0] == keys[1] {
			t.Fatalf("acme and evil reached the vendor's cache under one key %q", keys[0])
		}
		if keys[0] != keys[2] {
			t.Fatalf("acme's calls were keyed %q and %q, want one key", keys[0], keys[2])
		}
	})
	t.Run("at an address Hanzo runs", func(t *testing.T) {
		keys := ask(t, "", "acme")
		if len(keys) != 1 || keys[0] != "thread-1" {
			t.Fatalf("a Hanzo upstream was sent keys %q, want the caller's own", keys)
		}
	})
}
