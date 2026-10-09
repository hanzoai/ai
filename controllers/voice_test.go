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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"time"

	"github.com/golang-jwt/jwt/v4"
)

// Without an IAM to check a bearer against there must be no socket at all.
//
// This is the property worth holding: a WebSocket is exempt from the same-origin
// policy, so an ungated one is any page on the internet opening a microphone as
// whoever is signed in. Serving it "open for now" is not a smaller surface, it
// is the whole surface. Nil here, and the router registers nothing.
func TestVoiceIsNotServedWithoutIAM(t *testing.T) {
	t.Setenv("IAM_URL", "")
	if h := VoiceHandler(); h != nil {
		t.Fatal("a voice socket was offered with no IAM to gate it")
	}
}

// With one configured it answers, and health is reachable without a ticket —
// that is the endpoint a probe reads.
func TestVoiceServesHealthWhenGated(t *testing.T) {
	t.Setenv("IAM_URL", "https://hanzo.id")
	h := VoiceHandler()
	if h == nil {
		t.Fatal("no handler with an IAM configured")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/voice/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("health answered %d, want 200", rec.Code)
	}
}

// A session is minted from a bearer, so an anonymous POST must be refused
// rather than handed a ticket.
func TestVoiceSessionRefusesAnonymous(t *testing.T) {
	t.Setenv("IAM_URL", "https://hanzo.id")
	h := VoiceHandler()
	if h == nil {
		t.Fatal("no handler with an IAM configured")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/voice/session", nil))
	if rec.Code == http.StatusOK {
		t.Error("a session was minted for a request carrying no bearer")
	}
}

// The keys come from the IAM this pod reaches and the issuer a bearer names is the
// brand it was minted under, which are different addresses in production: the
// cloud pod reaches IAM at http://127.0.0.1:8000 while hanzo.id signs as
// https://hanzo.id. Taking the address for the issuer refused every real bearer.
func TestVoiceSessionAcceptsABearerFromTheBrandIssuer(t *testing.T) {
	p := withIAM(t)
	h := VoiceHandler()
	if h == nil {
		t.Fatal("no handler with an IAM configured")
	}
	// What IAM signs for a person: a subject and a membership set, home org first.
	bearer := func(issuer string) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss":  issuer,
			"sub":  "alice",
			"aud":  []string{iamTestAudience},
			"iat":  time.Now().Add(-time.Minute).Unix(),
			"exp":  time.Now().Add(time.Hour).Unix(),
			"orgs": []map[string]string{{"org": "acme", "role": "owner"}},
		})
		token.Header["kid"] = p.kid
		signed, err := token.SignedString(p.key)
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + signed
	}
	session := func(issuer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/voice/session", nil)
		req.Header.Set("Authorization", bearer(issuer))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := session("https://hanzo.id")
	if rec.Code != http.StatusOK {
		t.Fatalf("session answered %d %s, want 200 with a ticket", rec.Code, rec.Body)
	}
	var got struct{ Ticket string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Ticket == "" {
		t.Fatalf("no ticket in %s", rec.Body)
	}
	if rec := session("https://elsewhere.example"); rec.Code == http.StatusOK {
		t.Fatal("a bearer from an untrusted issuer was handed a ticket")
	}
}

// TestTalkModeThinksWithAModelTheCatalogServes. The default is the family's own
// name, which the zen catalog aliases to a model it serves; a retired id answers
// every turn with "chat 404" and no audio.
func TestTalkModeThinksWithAModelTheCatalogServes(t *testing.T) {
	t.Setenv("VOICE_MODEL", "")
	if got := voiceModel(); got != "zen" {
		t.Fatalf("talk mode thinks with %q, want zen", got)
	}
}
