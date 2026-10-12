package controllers

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// R1: the hold prices input at len(body)/4, but Zen's tokenizer reads a digit run
// one token a byte (Qwen3.8-27B: 12,000 digits are 12,000 tokens, 3.97x len/4).
// The service reports the router's prompt_tokens, so the settle passes the hold and
// the balance goes negative: the hold no longer bounds the spend.
func TestRedZenInputOutrunsHold(t *testing.T) {
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
		t.Fatalf("OVERDRAW: a %d¢ hold admitted a %.1f¢ call; balance %d¢", hold, float64(spent)/1e7, left)
	}
}

// R2: output_tokens is billed as reported, with no bound at the ceiling the hold
// priced: an upstream (a free third-party rung of zen6's chain, relayed by the
// router and by the service as u64) that reports 1e9 bills the caller $7,260 on a
// hold of 3¢; past ~1.27e15 the int64 product wraps and the bill is negative.
func TestRedOutputUnbounded(t *testing.T) {
	if got := decisionCostNano("zenlm/zen6", 300, 1_000_000_000); got <= 3*nanoPerCent {
		t.Fatalf("bounded already: %d", got)
	} else {
		t.Logf("1e9 output tokens bill %s USD against a %d¢ hold", nanoToUSD(got), 3)
	}
	if got := decisionCostNano("zenlm/zen6", 300, 1_300_000_000_000_000); got < 0 {
		t.Fatalf("OVERFLOW: 1.3e15 output tokens bill %d nano (%s USD)", got, nanoToUSD(got))
	}
}

// R2b: the same through the handler: the debit the books receive.
func TestRedOutputOverflowReachesDebit(t *testing.T) {
	fake, events := setupDecisions(t)
	fake.serve = func(string, []byte) (int, string) {
		return http.StatusOK, `{"id":"d","model":"zenlm/zen6","provider":"Hanzo","answers":{"q":{"type":"boolean","probability":1}},"usage":{"input_tokens":300,"output_tokens":1300000000000000},"routing":{"backend":"llm","checkpoint":"zen6","reason":"r"},"state_hash":"s","latency_ms":1}`
	}
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, `{"model":"zenlm/zen6","state":"x","questions":{"q":{"type":"boolean"}}}`); status != http.StatusOK {
		t.Fatalf("=> %d %s", status, body)
	}
	if len(*events) != 1 || strings.HasPrefix((*events)[0].USD, "-") {
		t.Fatalf("NEGATIVE DEBIT filed: %+v", *events)
	}
}

// R3: Kai's and Jev's bills, new arithmetic against the old (input alone), over
// every decision route the shipped catalog and the static table hold.
func TestRedKaiJevBillsUnchanged(t *testing.T) {
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

// R1b: 13,000 copies of 1e15 are a 65 KB body the gateway reads as 16k tokens; the
// service writes each as 1000000000000000.0, and Qwen3.8's tokenizer reads the task
// as 247,024 tokens (measured): a 7¢ hold, a 56.5¢ answer.
func TestRedExponentExpansionOutrunsHold(t *testing.T) {
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
		t.Fatalf("OVERDRAW: balance %d¢", left)
	}
}
