// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"math/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// Zen's tokenizer reads a digit run one token a byte (Qwen3.8-27B: 12,000 digits
// are 12,000 tokens, 3.97 times len/4), and the service reports the router's
// prompt_tokens. Held at a token a byte, the smallest balance that admits such a
// call is never overdrawn by it.
func TestZenDigitsNeverOverdrawAWallet(t *testing.T) {
	fake, _ := setupDecisions(t)
	r := rand.New(rand.NewSource(1))
	var digits strings.Builder
	for range 250_000 {
		digits.WriteByte(byte('0' + r.Intn(10)))
	}
	asked := `{"model":"zenlm/zen6","state":"` + digits.String() + `","questions":{"q":{"type":"boolean"}}}`
	// what the router reports: one token a digit, the system prompt, ~140 out.
	in, out := 250_000+160, 140
	fake.serve = func(string, []byte) (int, string) {
		return http.StatusOK, fmt.Sprintf(`{"id":"d","model":"zenlm/zen6","provider":"Hanzo","answers":{"q":{"type":"boolean","probability":1}},"usage":{"input_tokens":%d,"output_tokens":%d},"routing":{"backend":"llm","checkpoint":"zen6","reason":"r"},"state_hash":"s","latency_ms":1}`, in, out)
	}
	user, _ := providerKeyBillingUser(&object.Provider{Owner: decisionsOrg})
	sub := user.PayerSubject(decisionsOrg)
	hold := int64(1)
	fund(decisionsOrg, hold)
	for {
		if status, _ := driveDecisions(t, "Bearer "+decisionsKey, asked); status == http.StatusOK {
			break
		}
		hold++
		fund(decisionsOrg, hold)
	}
	left, _ := object.GlobalBalanceLedger.Available(sub)
	spent := decisionCostNano("zenlm/zen6", in, out)
	t.Logf("held %d¢, billed %.1f¢, balance after %d¢", hold, float64(spent)/1e7, left)
	if left < 0 {
		t.Fatalf("a %d¢ hold admitted a %.1f¢ call; balance %d¢", hold, float64(spent)/1e7, left)
	}
}

// output_tokens is what an upstream reported, relayed by the router and the
// service as an unsigned count: past the ceiling the hold priced it bills the
// ceiling, exactly, however large, where 1e9 once billed $7,260 on a 3¢ hold and
// past about 1.27e15 the int64 product wrapped negative.
func TestZenOutputBillsItsCeilingAtMost(t *testing.T) {
	ceil := decisionCostNano("zenlm/zen6", 300, decisionOutputTokens)
	for _, out := range []int{decisionOutputTokens + 1, 1_000_000_000, 1_300_000_000_000_000} {
		if got := decisionCostNano("zenlm/zen6", 300, out); got != ceil {
			t.Errorf("%d output tokens bill %d nano, want the ceiling's %d", out, got, ceil)
		}
	}
}

// The same through the handler: an overflowing count files no negative debit.
func TestAnOverflowingOutputFilesNoNegativeDebit(t *testing.T) {
	fake, events := setupDecisions(t)
	fake.serve = func(string, []byte) (int, string) {
		return http.StatusOK, `{"id":"d","model":"zenlm/zen6","provider":"Hanzo","answers":{"q":{"type":"boolean","probability":1}},"usage":{"input_tokens":300,"output_tokens":1300000000000000},"routing":{"backend":"llm","checkpoint":"zen6","reason":"r"},"state_hash":"s","latency_ms":1}`
	}
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, `{"model":"zenlm/zen6","state":"x","questions":{"q":{"type":"boolean"}}}`); status != http.StatusOK {
		t.Fatalf("=> %d %s", status, body)
	}
	if len(*events) != 1 || strings.HasPrefix((*events)[0].USD, "-") {
		t.Fatalf("a negative debit was filed: %+v", *events)
	}
}

// Kai's and Jev's bills and holds, new arithmetic against the old (input alone),
// over every decision route the shipped catalog and the static table hold.
func TestKaiAndJevBillAndHoldAsBefore(t *testing.T) {
	check := func(src string) {
		for _, id := range decisionModels() {
			if strings.HasPrefix(id, "zenlm/") {
				continue
			}
			for _, n := range [][2]int{{0, 0}, {1, 1}, {319, 23}, {42, 70}, {1_000_000, 4096}, {131_072, 1_000_000}} {
				old, now := tokenCostNano(id, n[0], 0, 0, 0), decisionCostNano(id, n[0], n[1])
				if old != now {
					t.Errorf("%s %s in=%d out=%d: old %d new %d", src, id, n[0], n[1], old, now)
				}
				hold0 := (tokenCostNano(id, n[0], 0, 0, 0) + nanoPerCent - 1) / nanoPerCent
				hold1 := (decisionCostNano(id, n[0], decisionOutputTokens) + nanoPerCent - 1) / nanoPerCent
				if hold0 != hold1 {
					t.Errorf("%s %s hold moved %d -> %d", src, id, hold0, hold1)
				}
			}
		}
	}
	check("static")
	useCatalog(t, "../conf/models.yaml")
	check("conf")
}

// 13,000 copies of 1e15 are a 65 KB body; the service writes each out again as
// 1000000000000000.0, and Qwen3.8's tokenizer reads the task as 247,024 tokens
// (measured): a 56.5¢ answer past any bound on the body. The wallet is not
// overdrawn, and the books are debited the hold.
func TestAnExpandedStateDebitsNoMoreThanItsHold(t *testing.T) {
	fake, events := setupDecisions(t)
	asked := `{"model":"zenlm/zen6","state":[` + strings.Repeat("1e15,", 12999) + `1e15],"questions":{"q":{"type":"boolean"}}}`
	fake.serve = func(string, []byte) (int, string) {
		return http.StatusOK, `{"id":"d","model":"zenlm/zen6","provider":"Hanzo","answers":{"q":{"type":"boolean","probability":1}},"usage":{"input_tokens":247184,"output_tokens":140},"routing":{"backend":"llm","checkpoint":"zen6","reason":"r"},"state_hash":"s","latency_ms":1}`
	}
	user, _ := providerKeyBillingUser(&object.Provider{Owner: decisionsOrg})
	sub := user.PayerSubject(decisionsOrg)
	fund(decisionsOrg, 1)
	for {
		if status, _ := driveDecisions(t, "Bearer "+decisionsKey, asked); status == http.StatusOK {
			break
		}
		b, _, _, _ := object.GlobalBalanceLedger.Snapshot(sub)
		fund(decisionsOrg, b+1)
	}
	left, _ := object.GlobalBalanceLedger.Available(sub)
	t.Logf("debit %s USD, balance after %d¢", (*events)[0].USD, left)
	if left < 0 {
		t.Fatalf("the wallet is overdrawn: %d¢", left)
	}
	// The books are debited the hold, not the answer's 56¢: the wallet the debit
	// lands on is the one the ledger mirrors.
	held := max((decisionCostNano("zenlm/zen6", len(asked)+decisionPromptTokens, decisionOutputTokens)+nanoPerCent-1)/nanoPerCent, 1)
	if got, want := (*events)[len(*events)-1].USD, nanoToUSD(held*nanoPerCent); got != want {
		t.Fatalf("debit %s USD past its %s USD hold", got, want)
	}
}

// The bound holds whoever pays: a plan that covers the call, which no wallet holds
// for, is settled and debited the hold too, never an inflated report.
func TestAnLLMDecisionBillsNoMoreThanItsHoldWhoeverPays(t *testing.T) {
	lanes(t)
	fake, events := setupDecisions(t)
	fake.answer = `{"id":"d","model":"zenlm/zen6","provider":"Hanzo","answers":{"q":{"type":"boolean","probability":1}},"usage":{"input_tokens":1000000,"output_tokens":4096},"routing":{"backend":"llm","checkpoint":"zen6","reason":"r"},"state_hash":"s","latency_ms":1}`
	asked := `{"model":"zenlm/zen6","state":"x","questions":{"q":{"type":"boolean"}}}`
	held := max((decisionCostNano("zenlm/zen6", len(asked)+decisionPromptTokens, decisionOutputTokens)+nanoPerCent-1)/nanoPerCent, 1) * nanoPerCent
	var paid []int64
	g := planGrant("max-20x", object.PaysPlan, object.ClassOurs)
	g.Settle = func(n int64) { paid = append(paid, n) }
	c := seatAs(t, presenting(visit(http.MethodPost, decisionsPath), "Bearer "+decisionsKey), party{grant: g, paid: true})
	c.Fiber().Request().SetBody([]byte(asked))
	if status := answering(t, c, c.Decisions); status != http.StatusOK {
		t.Fatalf("a covered decision => %d %s", status, sent(c))
	}
	settled(t)
	if len(*events) != 1 || !(*events)[0].Plan || (*events)[0].USD != nanoToUSD(held) {
		t.Fatalf("debits %+v, want one on the plan at the hold, %s USD", *events, nanoToUSD(held))
	}
	if len(paid) == 0 || paid[0] != held {
		t.Fatalf("the plan settled %v nano, want the hold, %d", paid, held)
	}
}
