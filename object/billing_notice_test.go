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

package object

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestInsufficientBalanceMessage: genuine insufficiency invites adding credits at the
// wallet link, embeds the per-modality noun, and carries the 402/insufficient_balance
// verdict. The plain form (noun "") is the router-filter copy.
func TestInsufficientBalanceMessage(t *testing.T) {
	n := InsufficientBalance("hanzo", "image cost")
	if n.Code != CodeInsufficientBalance {
		t.Errorf("code=%q, want %q", n.Code, CodeInsufficientBalance)
	}
	if n.Status != http.StatusPaymentRequired {
		t.Errorf("status=%d, want 402", n.Status)
	}
	if !strings.Contains(n.Message, "image cost") {
		t.Errorf("message must carry the noun, got %q", n.Message)
	}
	if !strings.Contains(n.Message, "Add credits") || !strings.Contains(n.Message, PayURL("hanzo")) {
		t.Errorf("message must invite adding credits at the wallet link, got %q", n.Message)
	}

	plain := InsufficientBalance("hanzo", "")
	if strings.Contains(plain.Message, "estimated") {
		t.Errorf("plain form must omit the noun clause, got %q", plain.Message)
	}
	if !strings.HasPrefix(plain.Message, "Insufficient balance. Add credits") {
		t.Errorf("plain form copy drift, got %q", plain.Message)
	}
}

// TestBalanceUnavailableMessage: a lookup failure is retryable (503) with the DISTINCT
// balance_unavailable code and, critically, does NOT tell the caller to add credits —
// the funded-wallet-402 fix. It carries no wallet link (adding credits is not the fix).
func TestBalanceUnavailableMessage(t *testing.T) {
	n := BalanceUnavailable()
	if n.Code != CodeBalanceUnavailable {
		t.Errorf("code=%q, want %q", n.Code, CodeBalanceUnavailable)
	}
	if n.Status != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503 (retryable)", n.Status)
	}
	if strings.Contains(strings.ToLower(n.Message), "add credits") {
		t.Errorf("transient denial must NOT say add credits, got %q", n.Message)
	}
	if strings.Contains(n.Message, "pay.hanzo.ai") {
		t.Errorf("transient denial carries no wallet link, got %q", n.Message)
	}
	if !strings.Contains(strings.ToLower(n.Message), "retry") {
		t.Errorf("transient denial should invite a retry, got %q", n.Message)
	}
}

// TestPayURL: the deep-link is the caller's OWN wallet, resolved by the pay SPA from
// the authenticated identity. The pay SPA has no /<org> route today (pay/src/main.tsx),
// so PayURL returns the bare wallet root for every org — never a broken /<org> path,
// and never another org's id in the URL. An anonymous denial (org "") degrades to the
// same bare link.
func TestPayURL(t *testing.T) {
	for _, org := range []string{"hanzo", "acme", "", "weird/slug with spaces", "a?b#c"} {
		if got := PayURL(org); got != payBaseURL {
			t.Errorf("PayURL(%q)=%q, want the bare wallet root %q (no /<org> route exists yet)", org, got, payBaseURL)
		}
	}
}

// TestErrorJSONShape locks the raw-HTTP wire shape the router balance filter emits:
// {"error":{"message":..,"type":"billing_error","code":..}}. The hanzo CLI switches on
// "code", so the keys and the always-"billing_error" type must not drift. The message
// is JSON-marshaled (never string-concatenated), so it stays correctly escaped.
func TestErrorJSONShape(t *testing.T) {
	for _, n := range []BillingNotice{InsufficientBalance("hanzo", "cost"), BalanceUnavailable()} {
		var parsed struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(n.ErrorJSON(), &parsed); err != nil {
			t.Fatalf("ErrorJSON is not valid JSON: %v (%s)", err, n.ErrorJSON())
		}
		if parsed.Error.Type != "billing_error" {
			t.Errorf("wire type=%q, want billing_error", parsed.Error.Type)
		}
		if parsed.Error.Code != n.Code {
			t.Errorf("wire code=%q, want %q", parsed.Error.Code, n.Code)
		}
		if parsed.Error.Message != n.Message {
			t.Errorf("wire message=%q, want %q (escaping must round-trip)", parsed.Error.Message, n.Message)
		}
	}
}
