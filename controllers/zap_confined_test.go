// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

// tokenScoped mints a token naming scope. Its signature is nobody's: the host
// verifier these tests install says yes to anything, so what is refused below is
// refused by the door, not by a verifier.
func tokenScoped(t *testing.T, scope string) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "https://hanzo.id", "sub": "uuid-alice", "aud": []string{"https://api.hanzo.ai"},
		"exp": time.Now().Add(time.Hour).Unix(), "scope": scope, "owner": "acme",
		"name": "alice", "tokenType": "access-token",
	}).SignedString([]byte("nobody's key"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A ZAP door has no boundary in front of it, so the host's verifier saying yes is
// not enough: every door, and every identity seam the doors share, refuses a token
// delegated for model calls outright.
func TestNoZapDoorServesAConfinedToken(t *testing.T) {
	t.Cleanup(func() { object.SetConfinedVerifier(nil) })
	object.SetConfinedVerifier(func(string) error { return nil })

	tok := tokenScoped(t, "ai:inference")
	if _, err := object.ParseAndValidateJWT(tok); err != nil {
		t.Fatalf("the installed verifier must admit the token, or nothing below is the door's doing: %v", err)
	}
	bearer := "Bearer " + tok
	ctx := context.Background()

	if _, err := zapResolveUser(bearer); !errors.Is(err, errZapConfined) {
		t.Errorf("zapResolveUser: %v, want %v", err, errZapConfined)
	}
	if _, _, _, err := zapResolveAuth(bearer, "zen-e2e"); !errors.Is(err, errZapConfined) {
		t.Errorf("zapResolveAuth: %v, want %v", err, errZapConfined)
	}
	if u := zapPrincipal(bearer); u != nil {
		t.Errorf("zapPrincipal made %s/%s of a confined token", u.Owner, u.Name)
	}

	// MsgType 100: response status(0) body(4) error(12).
	for _, method := range []string{"chat.completions", "balance", "models.list"} {
		out, err := handleCloudService(ctx, "peer", cloudCall(t, method, bearer, `{}`))
		if err != nil {
			t.Fatalf("cloud %s: %v", method, err)
		}
		if got, why := out.Root().Uint32(0), out.Root().Text(12); got != http.StatusUnauthorized || why != zapConfinedRefusal {
			t.Errorf("cloud %s = %d %q, want 401 %q", method, got, why, zapConfinedRefusal)
		}
	}

	// MsgType 110, the ops node: it deploys, so it is the last place one belongs.
	b := zap.NewBuilder(len(bearer) + 128)
	obj := b.StartObject(24)
	obj.SetText(object.CloudReqMethod, "status")
	obj.SetText(object.CloudReqAuth, bearer)
	obj.FinishAsRoot()
	ops, err := zap.Parse(b.FinishWithFlags(MsgTypeCloudOps << 8))
	if err != nil {
		t.Fatal(err)
	}
	out, err := handleCloudOps(ctx, "peer", ops)
	if err != nil {
		t.Fatalf("ops: %v", err)
	}
	if got := out.Root().Uint32(0); got != http.StatusUnauthorized {
		t.Errorf("ops status = %d, want 401", got)
	}

	// MsgType 200, the gateway. An unclaimed path falls back to the whole router,
	// which the installed verifier would let a confined token through; so would a
	// fast path. The token is refused wherever in the request it rides.
	reached := 0
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusTeapot)
	})
	gw := gateway(router)
	const probe = "/v1/zap-confined-probe/acme/thing"
	for name, req := range map[string]struct {
		path, query string
		headers     map[string]string
	}{
		"authorization": {probe, "", map[string]string{"Authorization": bearer}},
		"any scheme":    {probe, "", map[string]string{"Authorization": "bearer " + tok}},
		"x-api-key":     {probe, "", map[string]string{"x-api-key": tok}},
		"iam cookie":    {probe, "", map[string]string{"Cookie": "a=b; hanzo_iam_token=" + tok}},
		"query":         {probe, "x=1&access_token=" + strings.ReplaceAll(tok, ".", "%2E"), nil},
		"fast path":     {"/v1/chat/completions", "", map[string]string{"Authorization": bearer}},
		"balance":       {"/v1/balance", "", map[string]string{"Authorization": bearer}},
	} {
		out, err := gw(ctx, "peer", gatewayGet(t, req.path, req.query, req.headers, `{}`))
		if err != nil {
			t.Fatalf("gateway %s: %v", name, err)
		}
		if got := out.Root().Uint32(0); got != http.StatusUnauthorized {
			t.Errorf("gateway %s = %d, want 401", name, got)
		}
		if body := string(out.Root().Bytes(4)); !strings.Contains(body, zapConfinedRefusal) {
			t.Errorf("gateway %s answered %s, want it to say %q", name, body, zapConfinedRefusal)
		}
	}
	if reached != 0 {
		t.Errorf("the router was reached %d times by a confined token", reached)
	}

	// The forward bridge carries the client's whole header set into the router.
	bridge := unconfined(router)
	for name, set := range map[string]func(*http.Request){
		"authorization": func(r *http.Request) { r.Header.Set("Authorization", bearer) },
		"x-api-key":     func(r *http.Request) { r.Header.Set("X-Api-Key", tok) },
		"iam cookie":    func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "hanzo_iam_token", Value: tok}) },
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/ai/stores", nil)
		set(r)
		w := httptest.NewRecorder()
		bridge.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), zapConfinedRefusal) {
			t.Errorf("bridge %s = %d %s, want 401 %q", name, w.Code, w.Body.String(), zapConfinedRefusal)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/ai/stores?access_token="+tok, nil)
	w := httptest.NewRecorder()
	bridge.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bridge query = %d, want 401", w.Code)
	}
	if reached != 0 {
		t.Errorf("the router was reached %d times by a confined token", reached)
	}

	// Any other token still crosses every door as it did.
	plain := "Bearer " + tokenScoped(t, "openid profile")
	if out, err := gw(ctx, "peer", gatewayGet(t, probe, "", map[string]string{"Authorization": plain}, `{}`)); err != nil || out.Root().Uint32(0) != http.StatusTeapot {
		t.Errorf("an ordinary token through the gateway: err %v, want the router's 418", err)
	}
	r = httptest.NewRequest(http.MethodGet, "/v1/ai/stores", nil)
	r.Header.Set("Authorization", plain)
	w = httptest.NewRecorder()
	bridge.ServeHTTP(w, r)
	if w.Code != http.StatusTeapot {
		t.Errorf("an ordinary token through the bridge = %d, want the router's 418", w.Code)
	}
	if reached != 2 {
		t.Errorf("the router was reached %d times, want the two ordinary calls", reached)
	}
}
