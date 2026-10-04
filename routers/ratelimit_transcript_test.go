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

package routers

// A live transcript is one request to the rate and the quota.
//
// Dictation pushes pcm16 every 256 ms. The free rate's burst is twelve and its 8h
// quota 250, so counted as requests the pushes were refused after three seconds by
// one and after a minute of speech by the other — "Usage limit reached for this
// 8h" on a dictation while chat from the same caller still answered. The open is
// counted; a push or close the session table admits for the caller's own
// credential is not.

import (
	"net/http"
	"testing"

	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"
)

// sessions states which credential opened which transcript id, in the place the
// filter asks: controllers.TranscriptAdmitted, whose ownership rules have their own
// tests in controllers.
func sessions(t *testing.T, opened map[string]string) {
	t.Helper()
	prev := continues
	continues = func(method, path, auth string) bool {
		if method != http.MethodPost && method != http.MethodDelete {
			return false
		}
		owner, ok := opened[path]
		return ok && auth != "" && owner == auth
	}
	t.Cleanup(func() { continues = prev })
}

const pushPath = "/v1/audio/transcript/ats_test_1"

// TestATranscriptsPushesMeetNeitherCeiling: 300 pushes and a close, all served,
// and neither ceiling asked about any of them.
func TestATranscriptsPushesMeetNeitherCeiling(t *testing.T) {
	billing(t)
	seen := ceilings(t)
	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})
	sessions(t, map[string]string{pushPath: alice})

	for i := range 300 {
		p := ask(http.MethodPost, pushPath).with("Authorization", alice).body(make([]byte, 8192)).through(RateLimitFilter)
		if p.status() != http.StatusOK {
			t.Fatalf("push %d of 300 answered %d %s — a live transcript is spending the caller's rate or quota", i+1, p.status(), p.wrote)
		}
	}
	if p := ask(http.MethodDelete, pushPath).with("Authorization", alice).through(RateLimitFilter); p.status() != http.StatusOK {
		t.Fatalf("the close answered %d, want 200", p.status())
	}
	if rate, quota := seen(); rate != 0 || quota != 0 {
		t.Fatalf("the session's pushes met the rate %d time(s) and the quota %d time(s), want neither", rate, quota)
	}
}

// TestSomeoneElsesTranscriptMeetsBothCeilings. Naming a session another credential
// opened buys nothing: the request is counted like any other, so the free burst
// refuses it as it would refuse chat.
func TestSomeoneElsesTranscriptMeetsBothCeilings(t *testing.T) {
	billing(t)
	seen := ceilings(t)
	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})
	bob := authtest.Bearer(t, iam.User{Owner: "acme", Name: "bob"})
	sessions(t, map[string]string{pushPath: alice})

	refused := 0
	for range burst() + 1 {
		if ask(http.MethodPost, pushPath).with("Authorization", bob).through(RateLimitFilter).status() == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused == 0 {
		t.Fatalf("%d pushes to a session bob did not open were all admitted — the exemption was borrowed", burst()+1)
	}
	if rate, _ := seen(); rate != burst()+1 {
		t.Fatalf("the rate was asked %d time(s) about a stranger's pushes, want %d", rate, burst()+1)
	}
}

// TestATranscriptsOpenIsCounted. The open is the request a transcript is counted
// as, so it meets both ceilings.
func TestATranscriptsOpenIsCounted(t *testing.T) {
	billing(t)
	seen := ceilings(t)
	alice := authtest.Bearer(t, iam.User{Owner: "acme", Name: "alice"})
	sessions(t, map[string]string{pushPath: alice})

	p := ask(http.MethodPost, "/v1/audio/transcript").with("Authorization", alice).body([]byte(`{"model":"whisper-1"}`)).through(RateLimitFilter)
	if p.status() != http.StatusOK {
		t.Fatalf("the open answered %d, want 200", p.status())
	}
	if rate, quota := seen(); rate != 1 || quota != 1 {
		t.Fatalf("the open met the rate %d time(s) and the quota %d time(s), want 1 and 1", rate, quota)
	}
}

// TestATranscriptsPushesAreDecidedAtItsOpen. A push names no model, so the wallet
// gate cannot price it; its session was admitted at open, where the model is named.
// A caller holding nothing is not refused the session they opened, and a push to a
// session another credential opened still meets the wallet like any request.
func TestATranscriptsPushesAreDecidedAtItsOpen(t *testing.T) {
	gateWith(t, 0)
	sessions(t, map[string]string{pushPath: "Bearer tok"})

	for i := range 50 {
		p := ask(http.MethodPost, pushPath).with("Authorization", "Bearer tok").body(make([]byte, 8192)).through(BalanceGateFilter)
		if p.status() != http.StatusOK {
			t.Fatalf("push %d to the caller's own session answered %d %s — the open admitted it", i+1, p.status(), p.wrote)
		}
	}
	sessions(t, map[string]string{pushPath: "Bearer someone-else"})
	if p := ask(http.MethodPost, pushPath).with("Authorization", "Bearer tok").body(make([]byte, 8192)).through(BalanceGateFilter); p.status() != http.StatusPaymentRequired {
		t.Fatalf("a push to a session the caller did not open answered %d, want the wallet's 402", p.status())
	}
}
