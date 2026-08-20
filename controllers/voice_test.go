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
	"testing"
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
