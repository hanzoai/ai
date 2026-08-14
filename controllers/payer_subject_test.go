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
	// No name is written above: bind is the one place a record learns its principal,
	// so a literal that also spelled one out would be a second answer to the same
	// question — and the second answer is the one that used to be wrong.
	rec.bind(context.Background(), user)
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

// TestBindIsNilSafe: a surface that has no principal must leave the field
// unset rather than stamping a zero account, which the ledger would refuse.
func TestBindIsNilSafe(t *testing.T) {
	rec := &usageRecord{Owner: "acme", User: "acme/alice"}
	rec.bind(context.Background(), nil)
	if !rec.Payer.Zero() {
		t.Error("a nil principal must leave the payer unset so the fallback applies")
	}
	if got := rec.payer().Subject(); got != "acme" {
		t.Errorf("after a nil bind the fallback must still answer, got %q", got)
	}
}

// TestBindNamesOnlyAPerson is the attribution invariant, and it is the one that was
// costing money: a spend row may name a person or nobody, never a program.
//
// An application in the User column reads as attributed spend and is not. It is how
// 52% of a month's inference arrived on an invoice with a service account's name on
// it and no way to ask which human caused it — the query for unattributed spend
// found nothing, because every row was "attributed" to hanzo-cloud.
func TestBindNamesOnlyAPerson(t *testing.T) {
	person := &iam.User{Owner: "acme", Name: "alice"}
	machine := &iam.User{Owner: "hanzo", Name: "hanzo-cloud", Type: "application"}

	// A header naming someone else, present on every case, so each one answers the
	// same question: may THIS credential move its spend onto that name?
	onBehalf := object.WithGenAIAttribution(context.Background(),
		object.GenAIAttribution{User: "acme/bob"})

	for _, c := range []struct {
		name        string
		user        *iam.User
		ctx         context.Context
		wantUser    string
		wantAgent   string
		explanation string
	}{
		{
			name: "a person names themselves", user: person, ctx: context.Background(),
			wantUser: "acme/alice",
		},
		{
			name: "a header cannot move a person's spend", user: person, ctx: onBehalf,
			wantUser:    "acme/alice",
			explanation: "otherwise anyone could bill a colleague by sending a header",
		},
		{
			name: "a machine is an agent, and names the person it acts for",
			user: machine, ctx: onBehalf,
			wantUser: "acme/bob", wantAgent: "hanzo/hanzo-cloud",
		},
		{
			name: "a machine naming nobody says so", user: machine, ctx: context.Background(),
			wantUser: "", wantAgent: "hanzo/hanzo-cloud",
			explanation: "an empty column is a call a query can find",
		},
		{
			// THE LIVE SHAPE, and the reason typing the credential was not enough on
			// its own. A machine reaching us through the identity boundary never
			// chooses this header: the boundary deletes what arrived and rewrites it
			// from the presenting credential's own claims, so the machine is handed
			// ITS OWN subject and hands it straight back. The org halves differ —
			// `sub` is qualified by the registration's owner, the `owner` claim by the
			// org it serves — so only the name identifies the principal across them.
			name: "the boundary hands a machine its own subject",
			user: machine,
			ctx: object.WithGenAIAttribution(context.Background(),
				object.GenAIAttribution{User: "admin/hanzo-cloud"}),
			wantUser: "", wantAgent: "hanzo/hanzo-cloud",
			explanation: "a credential naming ITSELF has named no person; the application would be back in the user column one hop later",
		},
		{
			name: "a bare name is not an identity",
			user: machine,
			ctx: object.WithGenAIAttribution(context.Background(),
				object.GenAIAttribution{User: "bob"}),
			wantUser: "", wantAgent: "hanzo/hanzo-cloud",
			explanation: "'bob' in which org? refused rather than guessed at",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &usageRecord{Owner: c.user.Owner}
			rec.bind(c.ctx, c.user)

			if rec.User != c.wantUser {
				t.Errorf("named %q, want %q — %s", rec.User, c.wantUser, c.explanation)
			}
			if rec.Agent != c.wantAgent {
				t.Errorf("agent %q, want %q", rec.Agent, c.wantAgent)
			}
			// The whole point of separating the two columns: naming a person never
			// moves money. A machine's payer is its org no matter whose name it carries.
			if c.wantAgent != "" && rec.payer().Subject() != c.user.Owner {
				t.Errorf("a machine's payer moved to %q; attribution must not settle",
					rec.payer().Subject())
			}
		})
	}
}

// TestCloudAgentKeyIsAMachine covers the one credential this process ASSEMBLES
// rather than reads from IAM, so nothing upstream can type it and nothing
// downstream can tell it was not typed.
//
// Untyped it read as a person, and both halves went wrong at once. Attribution put
// the application in the user column. Money was worse: account.Payer's shape rule
// hands a person in the SIGNUP org a personal wallet, and "hanzo" is the signup
// org — so every call on this key addressed hanzo/cloud-agent, a wallet no funding
// path can name. It reads $0 forever while the org's balance sits one key away.
//
// It runs the REAL constructor (the key it accepts is the one it is given) and the
// REAL rules, so what is pinned is the identity this process actually hands the
// gate, not a literal that agrees with it today.
func TestCloudAgentKeyIsAMachine(t *testing.T) {
	t.Setenv("CLOUD_AGENT_KEY", "test-cloud-agent-key")

	agent := tryCloudAgentKeyFallback("test-cloud-agent-key")
	if agent == nil {
		t.Fatal("the fallback refused its own key; the rest of this test proves nothing")
	}
	if tryCloudAgentKeyFallback("some-other-key") != nil {
		t.Fatal("the fallback accepted a key it was not given")
	}

	for _, c := range []struct {
		name      string
		user      *iam.User
		wantUser  string
		wantAgent string
		wantPayer string
		why       string
	}{
		{
			name: "the cloud-agent key", user: agent,
			wantUser: "", wantAgent: "hanzo/cloud-agent", wantPayer: "hanzo",
			why: "a program's spend is nobody's, and it comes out of the org pool",
		},
		{
			name: "a person in the same org", user: &iam.User{Owner: "hanzo", Name: "alice"},
			wantUser: "hanzo/alice", wantAgent: "", wantPayer: "hanzo/alice",
			why: "the signup org's per-person wallet is right for the person it was written for",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &usageRecord{Owner: c.user.Owner}
			rec.bind(context.Background(), c.user)

			if rec.User != c.wantUser {
				t.Errorf("user = %q, want %q — %s", rec.User, c.wantUser, c.why)
			}
			if rec.Agent != c.wantAgent {
				t.Errorf("agent = %q, want %q", rec.Agent, c.wantAgent)
			}
			if got := rec.payer().Subject(); got != c.wantPayer {
				t.Errorf("payer = %q, want %q — %s", got, c.wantPayer, c.why)
			}
		})
	}
}
