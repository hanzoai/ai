// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package routers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/internal/authtest"
)

// X-Org-Id crosses the HTTP bridge: an admin of a member org, not an admin at
// home, is refused on its home org and admitted on the org it selects. Before the
// bridge carried the header, both requests asked about the home org.
func TestRouterPolicySelectedOrgCrossesBridge(t *testing.T) {
	t.Setenv("DISABLE_PREVIEW_MODE", "true")
	key := authtest.Signing(t)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"owner": "hanzo", "name": "z", "iss": "https://hanzo.id", "aud": "hanzo-cloud",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		"orgs": []map[string]string{{"org": "hanzo", "role": "admin"}, {"org": "webby-ai", "role": "admin"}},
	}).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	registerResources(app)
	registerAPI(app)
	get := func(org string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/v1/ai/router/policy", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if org != "" {
			req.Header.Set("X-Org-Id", org)
		}
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(""); code != http.StatusForbidden || !strings.Contains(body, "admin privilege") {
		t.Fatalf("home org: %d %s, want 403 admin privilege", code, body)
	}
	if code, body := get("webby-ai"); code == http.StatusForbidden {
		t.Fatalf("selected member org it administers: %d %s, want past the admin gate", code, body)
	}
	if code, body := get("globex"); code != http.StatusForbidden || !strings.Contains(body, "not available") {
		t.Fatalf("org outside the claim: %d %s, want 403 not available", code, body)
	}
}
