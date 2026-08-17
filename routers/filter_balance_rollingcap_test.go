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
	stdcontext "context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestRollingCapGate proves the plan's rolling-window AI-spend cap enforces on a
// metered WRITE even when the balance is sufficient, and — because it is a
// subscription-shaped limit — FAILS OPEN on any reader error and is a no-op when the
// host has not wired a reader. A caller WITH money is denied 429 usage_cap_exceeded
// only when the cap is positively exceeded; a finance blip (reader error) must never
// 429 a paying caller.
func TestRollingCapGate(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user") // resolveBillingKey → subject "acme"
	bg.ledger.SetBalance("acme", 100000)                       // $1000 spendable ⇒ balance is SUFFICIENT

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetRollingCapReader(nil) }) // never leak the hook into other tests

	post := func() (int, string) {
		p := ask(http.MethodPost, "/v1/chat/completions")
		p = p.with("Authorization", "Bearer tok")
		p = p.through(BalanceGateFilter)
		return p.status(), p.said()
	}

	// 1. Over the cap (reader returns over=true, no error) ⇒ 429 usage_cap_exceeded,
	//    distinct from the 402 insufficient_balance path (the caller HAS money).
	object.SetRollingCapReader(func(_ stdcontext.Context, subject, namespace string) (bool, error) {
		if subject != "acme" || namespace != "acme" {
			t.Errorf("reader got subject=%q namespace=%q, want acme/acme", subject, namespace)
		}
		return true, nil
	})
	if code, body := post(); code != http.StatusTooManyRequests {
		t.Errorf("over-cap POST must be 429, got %d (%s)", code, body)
	} else if !strings.Contains(body, "usage_cap_exceeded") {
		t.Errorf("over-cap body must carry code usage_cap_exceeded, got %s", body)
	}

	// 2. Under the cap (over=false) ⇒ admitted (no 429, no 402): balance covers it and
	//    the window budget is not spent.
	object.SetRollingCapReader(func(_ stdcontext.Context, _, _ string) (bool, error) {
		return false, nil
	})
	if code, body := post(); code == http.StatusTooManyRequests || code == http.StatusPaymentRequired {
		t.Errorf("under-cap POST must be admitted, got %d (%s)", code, body)
	}

	// 3. Reader error ⇒ FAIL OPEN (a commerce/finance blip must not 429 a paying
	//    caller). over is reported true but err is non-nil, so the gate ignores it.
	object.SetRollingCapReader(func(_ stdcontext.Context, _, _ string) (bool, error) {
		return true, errors.New("commerce unreachable")
	})
	if code, body := post(); code == http.StatusTooManyRequests {
		t.Errorf("reader error must FAIL OPEN (no 429), got %d (%s)", code, body)
	}

	// 4. No reader wired (standalone ai / unconfigured host) ⇒ no-op, admitted.
	object.SetRollingCapReader(nil)
	if code, body := post(); code == http.StatusTooManyRequests {
		t.Errorf("unwired reader must be a no-op (no 429), got %d (%s)", code, body)
	}
}
