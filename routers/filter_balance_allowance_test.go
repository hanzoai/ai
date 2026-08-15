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

import (
	stdcontext "context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	web "github.com/hanzoai/ai/web"

	"github.com/hanzoai/ai/object"
)

// A ZERO-PRICED ROUTE IS BOUNDED BY THE PLAN ALLOWANCE, AND BY NOTHING ELSE.
//
// The wallet has nothing to refuse at zero, so without this the free pool is
// unlimited for anyone who can name a free model. The allowance is a count of calls
// per subject per period; when it is spent the caller is refused 402 allowance_spent
// — a code distinct from insufficient_balance, because the caller is not broke, they
// are done for the period, and the cure is a plan rather than a top-up.
//
// It fails OPEN, and the direction is safe only here: the route costs nothing, so an
// unreadable allowance hands out our own compute and never a paid vendor call.
func TestAllowanceBoundsTheFreeRoute(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user") // resolveBillingKey → subject "acme"
	bg.ledger.SetBalance("acme", 0)                            // no wallet at all — the free caller

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetAllowance(nil) }) // never leak the hook into other tests

	post := func(body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		ctx := web.NewContext()
		ctx.Reset(rec, req)
		ctx.Input.CopyBody(1 << 20)
		BalanceGateFilter(ctx)
		return rec.Code, rec.Body.String()
	}

	free := `{"model":"enso-free","messages":[]}`

	// 1. No hook installed (standalone ai): the free route is served, unchanged.
	if code, _ := post(free); code == http.StatusPaymentRequired {
		t.Error("no allowance installed refused a free route — the default must be unchanged behavior")
	}

	// 2. Allowance left: served, and the counter saw the right subject and org.
	var saw struct{ subject, namespace string }
	object.SetAllowance(func(_ stdcontext.Context, subject, namespace string) (bool, error) {
		saw.subject, saw.namespace = subject, namespace
		return false, nil
	})
	if code, _ := post(free); code == http.StatusPaymentRequired {
		t.Error("a caller with allowance left was refused")
	}
	if saw.subject != "acme" || saw.namespace != "acme" {
		t.Errorf("allowance got subject=%q namespace=%q, want acme/acme", saw.subject, saw.namespace)
	}

	// 3. Allowance spent: 402 with the distinct code, so the product can offer a plan
	//    rather than a top-up.
	object.SetAllowance(func(_ stdcontext.Context, _, _ string) (bool, error) { return true, nil })
	code, body := post(free)
	if code != http.StatusPaymentRequired {
		t.Fatalf("a spent allowance returned %d, want 402", code)
	}
	if !strings.Contains(body, `"code":"allowance_spent"`) {
		t.Errorf("refusal body %q does not carry code allowance_spent", body)
	}
	if strings.Contains(body, "insufficient_balance") {
		t.Error("a spent allowance was reported as an empty wallet — the two have different cures")
	}

	// 4. Reader error: fails OPEN. The route costs nothing, so an unreadable
	//    allowance can only ever hand out our own compute.
	object.SetAllowance(func(_ stdcontext.Context, _, _ string) (bool, error) {
		return true, errors.New("store unreachable")
	})
	if code, _ := post(free); code == http.StatusPaymentRequired {
		t.Error("an allowance read error refused a free route — it must fail open")
	}
}

// THE ALLOWANCE NEVER TOUCHES A PRICED ROUTE. Money bounds those, and a counter that
// also refused them would be a second paywall with its own opinion — including one
// that could ADMIT a call the wallet refuses. The gate order proves it: a priced
// model at $0 is 402 insufficient_balance whatever the allowance says.
func TestAllowanceLeavesThePaywallAlone(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user")
	bg.ledger.SetBalance("acme", 0)

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetAllowance(nil) })

	called := false
	object.SetAllowance(func(_ stdcontext.Context, _, _ string) (bool, error) {
		called = true
		return false, nil // "allowance left" must not rescue a caller with no money
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	ctx := web.NewContext()
	ctx.Reset(rec, req)
	ctx.Input.CopyBody(1 << 20)
	BalanceGateFilter(ctx)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("a priced model at $0 returned %d, want 402 — the paywall must hold", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "insufficient_balance") {
		t.Errorf("refusal body %q is not the wallet's — a priced route must refuse as unpaid", rec.Body.String())
	}
	if called {
		t.Error("the allowance was counted against a PRICED call — it bounds the free lane only")
	}
}
