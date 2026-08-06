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
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// gateAndDebit runs the two halves of one request's money path against the same
// principal and reports the account EACH of them addressed: the subject the balance
// gate READ, and the subject the usage debit DRAINED.
//
// They are supposed to be one account. Everything below is a way of asking whether
// they are.
func gateAndDebit(t *testing.T, user *iam.User, ledger string) (gated, debited string) {
	t.Helper()

	// The gate's read.
	prevBalance := object.BalanceReader()
	object.SetBalanceReader(func(_ context.Context, subject, namespace, currency string) (int64, error) {
		gated = subject
		return 100_00, nil
	})
	t.Cleanup(func() { object.SetBalanceReader(prevBalance) })

	// The debit's write.
	prevUsage := object.UsageRecorder()
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		debited = u.Subject
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prevUsage) })

	if err := enforceBalanceGate(user, ledger, "gpt-4"); err != nil {
		t.Fatalf("enforceBalanceGate: %v", err)
	}

	org := ledger
	if org == "" {
		org = user.Owner
	}
	rec := &usageRecord{
		Owner:            org,
		User:             user.Owner + "/" + user.Name,
		Organization:     user.Owner,
		Model:            "gpt-4",
		Provider:         "openai",
		PromptTokens:     1000,
		CompletionTokens: 1000,
		TotalTokens:      2000,
		Currency:         "USD",
		Status:           "success",
		RequestID:        "req-1",
	}
	rec.stampPayer(user)
	recordUsage(rec)

	return gated, debited
}

// TestGateAndDebitAddressOneAccount is the invariant: for any principal, the wallet
// the gate reads and the wallet the debit drains are the same wallet.
//
// Each case below is a principal whose payer the gate resolves from the FULL
// credential — the signed billing_account claim, the credential's machine-ness — and
// whose debit used to be re-derived from the two strings on the usage record, which
// carry neither. On the divergent ones the gate checked a funded account and the
// debit drained a different one: a funded caller 402s while an unfunded one runs.
func TestGateAndDebitAddressOneAccount(t *testing.T) {
	for _, c := range []struct {
		name   string
		user   *iam.User
		ledger string
		want   string
	}{
		{
			// The claim names a PERSONAL wallet inside a pooled org. The gate honors
			// it; the two strings on the record cannot express it, so the debit fell
			// back to the org pool.
			name:   "personal billing claim",
			user:   &iam.User{Owner: "acme", Name: "alice", BillingAccount: "person:acme/alice"},
			ledger: "acme",
			want:   "acme/alice",
		},
		{
			// A machine credential in the shared signup org. The gate resolves the org
			// pool from Machine; the record has no machine flag, so the debit fell
			// through to the signup org's per-person rule and drained a personal
			// wallet instead.
			name:   "machine credential in the signup org",
			user:   &iam.User{Owner: "hanzo", Name: "svc-indexer", Type: "application"},
			ledger: "hanzo",
			want:   "hanzo",
		},
		{
			// The claim names the org pool for a member of the signup org, where the
			// pre-claim default is the personal wallet.
			name:   "org claim inside the signup org",
			user:   &iam.User{Owner: "hanzo", Name: "bob", BillingAccount: "org:hanzo"},
			ledger: "hanzo",
			want:   "hanzo",
		},
		{
			// An org switch: the claim names the home org, so it is discarded and the
			// SELECTED org's ledger pays. Both halves must agree on that.
			name:   "switched org discards a home-org claim",
			user:   &iam.User{Owner: "acme", Name: "alice", BillingAccount: "person:acme/alice"},
			ledger: "globex",
			want:   "globex",
		},
		{
			// The unchanged majority: a plain member of a pooled org, no claim. This
			// one already agreed, and must still.
			name:   "pooled org, no claim",
			user:   &iam.User{Owner: "acme", Name: "alice"},
			ledger: "acme",
			want:   "acme",
		},
		{
			// The other unchanged case: the signup org's per-person default.
			name:   "signup org, no claim",
			user:   &iam.User{Owner: "hanzo", Name: "carol"},
			ledger: "hanzo",
			want:   "hanzo/carol",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			gated, debited := gateAndDebit(t, c.user, c.ledger)

			if gated != c.want {
				t.Fatalf("the gate read %q, want %q — the test's own premise is wrong", gated, c.want)
			}
			if debited != gated {
				t.Errorf("gate read %q but the debit drained %q; one request, one account", gated, debited)
			}
		})
	}
}

// TestUnattributedRecordFallsBackUnchanged covers the surfaces with no authenticated
// principal to stamp — the session-scoped and self-billing ones. They carry no payer,
// so they resolve exactly as every debit did before, from the record's two strings.
func TestUnattributedRecordFallsBackUnchanged(t *testing.T) {
	for _, c := range []struct{ owner, user, want string }{
		{"acme", "acme/alice", "acme"},
		{"hanzo", "hanzo/alice", "hanzo/alice"},
		{"", "acme/alice", "acme"},
	} {
		rec := &usageRecord{Owner: c.owner, User: c.user}
		if got := rec.payer().Subject(); got != c.want {
			t.Errorf("unstamped record %q/%q resolves to %q, want the unchanged %q", c.owner, c.user, got, c.want)
		}
	}
}

// TestStampPayerIsNilSafe: a surface that has no principal must leave the field
// unset rather than stamping a zero account, which the ledger would refuse.
func TestStampPayerIsNilSafe(t *testing.T) {
	rec := &usageRecord{Owner: "acme", User: "acme/alice"}
	rec.stampPayer(nil)
	if !rec.Payer.Zero() {
		t.Error("a nil principal must leave the payer unset so the fallback applies")
	}
	if got := rec.payer().Subject(); got != "acme" {
		t.Errorf("after a nil stamp the fallback must still answer, got %q", got)
	}
}
