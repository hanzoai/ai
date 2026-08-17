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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/ai/funding"
	"github.com/hanzoai/ai/internal/iam"
)

// The cash breaker has never been observed to fire anywhere but in its own unit
// test, because nothing has ever published a State into the process that reads
// one. That makes the interesting question not "does Refuse() compute the right
// boolean" — funding/funding_test.go settles that — but "does a published State
// actually turn into a refusal AT THE GATE, ahead of the balance read".
//
// These exercise the real enforceBalanceGate against a FUNDED caller, so the only
// thing that can refuse is the breaker.

// fundedLedger stands up a commerce balance backend that answers "rich" and
// counts how many times it was asked. The count is the ordering proof: the
// breaker is documented to run BEFORE the balance read, and a gate that refused
// only after paying for a lookup would be reading a wallet it had already
// decided not to spend from.
func fundedLedger(t *testing.T) *atomic.Int64 {
	t.Helper()
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available": 100000}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("commerceEndpoint", srv.URL)
	t.Setenv("commerceToken", "test-svc-token")
	return &reads
}

// armBreaker publishes a State and guarantees the process is left disarmed, so a
// breaker armed by one test can never leak into another and refuse it.
func armBreaker(t *testing.T, s funding.State) {
	t.Helper()
	funding.Publish(s)
	t.Cleanup(func() { funding.Publish(funding.State{}) })
}

// TestBreakerRefusesAFundedCallerAtTheCeiling is the proof the breaker works:
// same caller, same funded ledger, one difference — a published ceiling that has
// been reached — and the gate turns from admit to refuse.
//
// The refusal is 503, and the caller it lands on is the whole argument. This test
// runs a FUNDED caller precisely because that is who the breaker stops: the
// ceiling is a fact about the Hanzo bank account, so the one person guaranteed to
// meet it is somebody whose own wallet is fine. 402 tells them otherwise.
func TestBreakerRefusesAFundedCallerAtTheCeiling(t *testing.T) {
	user := &iam.User{Owner: "acme", Name: "bob"}

	// Control: disarmed, the funded caller is admitted.
	reads := fundedLedger(t)
	armBreaker(t, funding.State{})
	if err := enforceBalanceGate(user, "acme", "glm-5.2"); err != nil {
		t.Fatalf("disarmed breaker refused a funded caller: %v", err)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("balance read count = %d, want 1 (the gate must read the wallet when it admits)", got)
	}

	// Armed and at the ceiling: the SAME funded caller is refused.
	reads = fundedLedger(t)
	armBreaker(t, funding.State{
		OnCash:       true,
		TodayCents:   250_00,
		CeilingCents: 200_00,
		Until:        time.Now().Add(time.Hour),
	})
	err := enforceBalanceGate(user, "acme", "glm-5.2")
	if err == nil {
		t.Fatal("breaker at its ceiling admitted a request — the ceiling is not enforced")
	}

	ae, ok := err.(*apiError)
	if !ok {
		t.Fatalf("breaker refusal is not a typed apiError: %T %v", err, err)
	}
	if ae.status == http.StatusPaymentRequired {
		t.Fatal("the breaker billed OUR ceiling to a caller holding $1,000 — 402 says they owe " +
			"money, and a client that acts on one goes to top up a balance that is already funded")
	}
	if ae.status != http.StatusServiceUnavailable {
		t.Fatalf("breaker refusal status = %d, want %d", ae.status, http.StatusServiceUnavailable)
	}
	if ae.code != codeSupply {
		t.Fatalf("breaker refusal code = %q, want %q — the status is what a person sees and the "+
			"code is what a client acts on, so both have to say whose problem this is", ae.code, codeSupply)
	}

	// The numbers are ours. They are what we pay to buy inference, they reach an
	// operator through the counter and the treasury board, and a customer reading a
	// refusal is not the audience for our cash position.
	for _, ours := range []string{"250.00", "200.00", "CLOUD_DAILY_CASH_CEILING_CENTS"} {
		if strings.Contains(err.Error(), ours) {
			t.Errorf("the refusal a customer reads discloses %q: %q", ours, err.Error())
		}
	}

	// ORDERING: the wallet was never opened. The breaker is a property of OUR bank
	// account, not the caller's, so it must not cost a lookup to enforce.
	if got := reads.Load(); got != 0 {
		t.Fatalf("balance read count = %d, want 0 (the breaker must refuse before the balance read)", got)
	}
}

// TestBreakerDoesNotRefuseBelowTheCeiling keeps the arming honest: a ceiling that
// is set but not reached must be invisible.
func TestBreakerDoesNotRefuseBelowTheCeiling(t *testing.T) {
	fundedLedger(t)
	armBreaker(t, funding.State{
		OnCash:       true,
		TodayCents:   199_99,
		CeilingCents: 200_00,
		Until:        time.Now().Add(time.Hour),
	})
	if err := enforceBalanceGate(&iam.User{Owner: "acme", Name: "bob"}, "acme", "glm-5.2"); err != nil {
		t.Fatalf("breaker refused a cent below its ceiling: %v", err)
	}
}

// TestBreakerStillOnPromoCreditDoesNotRefuse pins the other disarming arm: while
// a grant still covers usage, spend is not cash and the ceiling does not apply.
func TestBreakerStillOnPromoCreditDoesNotRefuse(t *testing.T) {
	fundedLedger(t)
	armBreaker(t, funding.State{
		OnCash:       false,
		TodayCents:   900_00,
		CeilingCents: 200_00,
		Until:        time.Now().Add(time.Hour),
	})
	if err := enforceBalanceGate(&iam.User{Owner: "acme", Name: "bob"}, "acme", "glm-5.2"); err != nil {
		t.Fatalf("breaker refused while still on promo credit: %v", err)
	}
}

// TestStaleBreakerReadingAdmits is the availability property, proven at the gate
// rather than on the value.
//
// This is the case that decides whether the ceiling can be armed at all. The
// publisher is a background refresh; when it stops — the process that computes
// the treasury numbers is down, redeployed, or simply never ran — the last thing
// it said must not become permanent. A reading that refuses and then goes stale
// has to admit again, or one bad afternoon for the finance aggregation is a
// fleet-wide outage of paid inference.
func TestStaleBreakerReadingAdmits(t *testing.T) {
	reads := fundedLedger(t)
	armBreaker(t, funding.State{
		OnCash:       true,
		TodayCents:   250_00, // well over
		CeilingCents: 200_00,
		Until:        time.Now().Add(-time.Minute), // ...but nobody has said so lately
	})
	if err := enforceBalanceGate(&iam.User{Owner: "acme", Name: "bob"}, "acme", "glm-5.2"); err != nil {
		t.Fatalf("a stale breaker reading refused a funded caller: %v", err)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("balance read count = %d, want 1 (a stale breaker must fall through to the real gate)", got)
	}
}
