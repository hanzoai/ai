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

// TestEnforceBalanceGate_M1 pins the prepaid-balance + premium-credit policy that
// the provider-key (sk-) path now shares with the JWT/IAM path (M1). The decisive
// row: a PREMIUM model with a balance at/under the starter credit is BLOCKED (402)
// — previously the sk- path skipped this gate entirely, so an sk- key reached
// premium upstreams on starter credit alone. StarterCreditDollars is pinned to the
// $5.00 const by nil'ing globalModelConfig, so the boundary is deterministic.
func TestEnforceBalanceGate_M1(t *testing.T) {
	prev := globalModelConfig
	globalModelConfig = nil // pin starter credit to StarterCreditDollars ($5.00)
	t.Cleanup(func() { globalModelConfig = prev })

	var available int64
	t.Setenv("commerceEndpoint", fakeCommerceBalance(t, &available))
	t.Setenv("commerceToken", "test-svc-token")
	t.Setenv("BALANCE_EXEMPT_USERS", "") // the test principal must NOT be exempt

	// sk- billing owner: org-scoped M2M principal (Name empty → org ledger).
	skOwner := func() *iam.User { return &iam.User{Owner: "m1-acme", Type: "application"} }

	const ok = http.StatusOK // sentinel: "no error / allowed"
	cases := []struct {
		name    string
		cents   int64
		premium bool
		want    int
	}{
		{"premium blocked at exactly the starter credit ($5.00)", 500, true, http.StatusPaymentRequired},
		{"premium blocked below starter ($1.00)", 100, true, http.StatusPaymentRequired},
		{"premium allowed above starter ($5.01)", 501, true, ok},
		{"premium allowed with real balance ($1000)", 100000, true, ok},
		// The same $5 balance is FINE for a non-premium model — proves it is the
		// PREMIUM rule blocking above, not merely balance>0.
		{"non-premium allowed at starter credit ($5.00)", 500, false, ok},
		{"non-premium blocked at zero balance", 0, false, http.StatusPaymentRequired},
		{"non-premium allowed with balance ($10)", 1000, false, ok},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			available = tc.cents
			err := enforceBalanceGate(skOwner(), "glm-5.2", tc.premium)
			if tc.want == ok {
				if err != nil {
					t.Fatalf("want allowed, got blocked: %v (status %d)", err, statusOf(err))
				}
				return
			}
			if err == nil {
				t.Fatalf("want blocked (%d), got allowed (nil) — M1 gate bypass", tc.want)
			}
			if got := statusOf(err); got != tc.want {
				t.Fatalf("status=%d, want %d (err=%v)", got, tc.want, err)
			}
		})
	}
}

// TestEnforceBalanceGate_FailClosedOnLookupError proves the gate never grants when
// the balance cannot be verified: an unreachable commerce endpoint → 500, not a
// silent pass.
func TestEnforceBalanceGate_FailClosedOnLookupError(t *testing.T) {
	prev := globalModelConfig
	globalModelConfig = nil
	t.Cleanup(func() { globalModelConfig = prev })

	t.Setenv("commerceEndpoint", "") // unconfigured → getUserBalance errors
	t.Setenv("BALANCE_EXEMPT_USERS", "")

	err := enforceBalanceGate(&iam.User{Owner: "m1-noverify", Type: "application"}, "glm-5.2", true)
	if err == nil {
		t.Fatal("balance unverifiable must fail closed, got nil (would grant free access)")
	}
	if got := statusOf(err); got != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (fail-closed on lookup error); err=%v", got, err)
	}
}

// TestEnforceBalanceGate_NoExemptBypass proves the exempt concept is GONE: a
// principal formerly listed in BALANCE_EXEMPT_USERS no longer bypasses. The gate
// ALWAYS consults the prepaid balance, so with commerce unreachable a
// formerly-exempt principal fails CLOSED (500) — never a free pass. All AI is
// prepaid; nothing is exempt.
func TestEnforceBalanceGate_NoExemptBypass(t *testing.T) {
	prev := globalModelConfig
	globalModelConfig = nil
	t.Cleanup(func() { globalModelConfig = prev })

	t.Setenv("commerceEndpoint", "")              // gate must still consult balance → errors
	t.Setenv("BALANCE_EXEMPT_USERS", "m1-exempt") // set but IGNORED: the concept is removed

	err := enforceBalanceGate(&iam.User{Owner: "m1-exempt", Type: "application"}, "glm-5.2", true)
	if err == nil {
		t.Fatal("no principal may bypass the prepaid gate; formerly-exempt must fail closed, got nil")
	}
	if got := statusOf(err); got != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (fail-closed, no exempt bypass); err=%v", got, err)
	}
}
