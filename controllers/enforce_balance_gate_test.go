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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/iam"
)

// fakeCommerceBalance stands up a stub of commerce GET /v1/billing/balance that
// reports availableCents, the value getUserBalance reads. Returns the server URL.
func fakeCommerceBalance(t *testing.T, availableCents *int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"available": %d}`, *availableCents)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestEnforceBalanceGate pins the prepaid-balance policy AFTER the implicit free
// tier was removed: EVERY model needs a positive prepaid balance and nothing more.
// A zero (or negative) balance is refused (402) with NO implicit free allowance;
// any positive balance is allowed. There is no per-model tier and no starter-credit
// fence, so the SAME balance clears a premium and a non-premium model identically —
// a subject's balance is only ever what it paid or was explicitly granted. The gate
// is shared by the JWT/IAM and provider-key (sk-) paths (M1), so this one policy
// covers both auth paths.
func TestEnforceBalanceGate(t *testing.T) {
	var available int64
	t.Setenv("commerceEndpoint", fakeCommerceBalance(t, &available))
	t.Setenv("commerceToken", "test-svc-token")

	// org-scoped M2M principal (Name empty → org ledger).
	principal := func() *iam.User { return &iam.User{Owner: "gate-acme", Type: "application"} }

	const ok = http.StatusOK // sentinel: "allowed / no error"
	cases := []struct {
		name  string
		cents int64
		want  int
	}{
		{"zero balance blocked (no implicit free allowance)", 0, http.StatusPaymentRequired},
		{"negative balance blocked", -100, http.StatusPaymentRequired},
		// $5 — formerly the starter-credit ceiling that fenced premium models — is
		// now just $5 of real, paid/granted credit and clears ANY model.
		{"small balance ($5.00) allowed", 500, ok},
		{"one cent allowed", 1, ok},
		{"real balance ($1000) allowed", 100000, ok},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			available = tc.cents
			err := enforceBalanceGate(principal(), "glm-5.2")
			if tc.want == ok {
				if err != nil {
					t.Fatalf("want allowed, got blocked: %v (status %d)", err, statusOf(err))
				}
				return
			}
			if err == nil {
				t.Fatalf("want blocked (%d), got allowed (nil) — implicit free credit", tc.want)
			}
			if got := statusOf(err); got != tc.want {
				t.Fatalf("status=%d, want %d (err=%v)", got, tc.want, err)
			}
		})
	}
}

// TestEnforceBalanceGate_NoAutoGrantOnFirstUse proves the gate has NO ambient-credit
// side effect: a first request from a brand-new zero-balance subject is refused
// (402) and the ONLY thing ai does to commerce is the read-only balance lookup
// (GET /v1/billing/balance). There is no first-use auto-mint — ai never POSTs a
// credit/grant, so a new subject stays at $0 until credit is explicitly granted or
// paid. (Onboarding credit, if ever wanted, must be an explicit policy-gated call
// to commerce POST /v1/billing/credit, never an implicit grant on first use.)
func TestEnforceBalanceGate_NoAutoGrantOnFirstUse(t *testing.T) {
	var mu sync.Mutex
	var calls []string // "METHOD PATH" of every commerce request ai made
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		// New subject: commerce reports a zero balance and is NOT auto-topped-up.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"available": 0}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("commerceEndpoint", srv.URL)
	t.Setenv("commerceToken", "test-svc-token")

	newSubject := &iam.User{Owner: "brand-new-org", Name: "first-timer", Type: "user"}
	err := enforceBalanceGate(newSubject, "glm-5.2")

	// Refused, not handed free usage.
	if err == nil {
		t.Fatal("zero-balance first request must be refused (402), got allowed — implicit free credit")
	}
	if got := statusOf(err); got != http.StatusPaymentRequired {
		t.Fatalf("status=%d, want 402 (insufficient balance); err=%v", got, err)
	}

	// The ONLY commerce interaction is the read-only balance lookup. Any write, or
	// any /credit|/grant|/starter path, would be a first-use auto-mint.
	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("expected the gate to read the balance from commerce, saw no call")
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "GET ") {
			t.Fatalf("gate made a non-GET commerce call %q — a first-use auto-grant/mint side effect", c)
		}
		if strings.Contains(c, "credit") || strings.Contains(c, "grant") || strings.Contains(c, "starter") {
			t.Fatalf("gate hit a credit/grant endpoint %q — implicit onboarding grant must not exist", c)
		}
		if !strings.Contains(c, "/v1/billing/balance") {
			t.Fatalf("unexpected commerce call %q — the gate must only READ balance", c)
		}
	}
}

// TestEnforceBalanceGate_GatedSKUIsPaid pins that Enso (a gated family SKU) is PAID:
// the comp bypass is GONE, so a gated SKU is gated on the prepaid balance exactly like
// any other model. Access-gating (the grant) controls whether the SKU is callable; it
// never makes the call free. Seeded gated so the SKU is genuinely limited-preview.
func TestEnforceBalanceGate_GatedSKUIsPaid(t *testing.T) {
	savedEnso := ensoFam.byID
	ensoFam.byID = map[string]zenModel{"enso": {ID: "enso", Access: "waitlist"}}
	t.Cleanup(func() { ensoFam.byID = savedEnso })
	if !FamilyModelGated("enso") {
		t.Fatal("precondition: enso must be a gated SKU for this pin to be meaningful")
	}

	var available int64
	t.Setenv("commerceEndpoint", fakeCommerceBalance(t, &available))
	t.Setenv("commerceToken", "test-svc-token")

	user := &iam.User{Owner: "enso-preview", Name: "granted", Email: "g@hanzo.ai", Type: "normal-user"}

	// Zero balance on the gated SKU → 402. No comp escape exists.
	available = 0
	if err := enforceBalanceGate(user, "enso"); err == nil {
		t.Fatal("gated SKU at $0 balance must 402 — the comp bypass is removed (Enso is PAID)")
	} else if got := statusOf(err); got != http.StatusPaymentRequired {
		t.Fatalf("status=%d, want 402 for the gated SKU at $0; err=%v", got, err)
	}

	// Funded → allowed, same as any paid model.
	available = 100000
	if err := enforceBalanceGate(user, "enso"); err != nil {
		t.Fatalf("funded caller must be allowed on the gated SKU, got blocked: %v", err)
	}
}

// TestEnforceBalanceGate_FailClosedOnLookupError proves the gate never grants when
// the balance cannot be verified: an unreachable commerce endpoint → 500, not a
// silent pass.
func TestEnforceBalanceGate_FailClosedOnLookupError(t *testing.T) {
	t.Setenv("commerceEndpoint", "") // unconfigured → getUserBalance errors

	err := enforceBalanceGate(&iam.User{Owner: "gate-noverify", Type: "application"}, "glm-5.2")
	if err == nil {
		t.Fatal("balance unverifiable must fail closed, got nil (would grant free access)")
	}
	if got := statusOf(err); got != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (fail-closed on lookup error); err=%v", got, err)
	}
}

// TestEnforceBalanceGate_NoExemptBypass proves there is no exempt principal: the
// gate ALWAYS consults the prepaid balance, so with commerce unreachable a
// formerly-"exempt" principal fails CLOSED (500) — never a free pass. All AI is
// prepaid; nothing is exempt and nothing is implicitly free.
func TestEnforceBalanceGate_NoExemptBypass(t *testing.T) {
	t.Setenv("commerceEndpoint", "")                // gate must still consult balance → errors
	t.Setenv("BALANCE_EXEMPT_USERS", "gate-exempt") // set but IGNORED: the concept is removed

	err := enforceBalanceGate(&iam.User{Owner: "gate-exempt", Type: "application"}, "glm-5.2")
	if err == nil {
		t.Fatal("no principal may bypass the prepaid gate; formerly-exempt must fail closed, got nil")
	}
	if got := statusOf(err); got != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (fail-closed, no exempt bypass); err=%v", got, err)
	}
}
