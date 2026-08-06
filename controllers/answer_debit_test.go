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
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// countingProvider is a model.ModelProvider that answers a fixed string and counts
// how many completions it was asked for. It is the instrument for "one request, one
// completion": a second upstream call shows up here as a second count.
type countingProvider struct {
	calls    int
	dryRuns  int
	answer   string
	price    float64
	currency string
}

func (p *countingProvider) GetPricing() string { return "" }

func (p *countingProvider) QueryText(question string, writer io.Writer, history []*model.RawMessage, prompt string, knowledge []*model.RawMessage, agentInfo *model.AgentInfo, lang string) (*model.ModelResult, error) {
	if strings.HasPrefix(question, model.DryRunPrefix) {
		p.dryRuns++
	} else {
		p.calls++
		_, _ = writer.Write([]byte("event: message\ndata: " + p.answer + "\n\n"))
	}
	currency := p.currency
	if currency == "" {
		currency = "USD"
	}
	return &model.ModelResult{
		PromptTokenCount:   100,
		ResponseTokenCount: 200,
		TotalTokenCount:    300,
		TotalPrice:         p.price,
		Currency:           currency,
	}, nil
}

// answerProvider is the provider row the gate reads Type/SubType/Name off. "OpenAI"
// + a non-reasoning subtype is the shape that DOES dry run, so the gate is armed.
func answerProvider() *object.Provider {
	return &object.Provider{Name: "openai-gpt4", Type: "OpenAI", SubType: "gpt-4"}
}

// balanceOf installs a native balance reader answering a fixed number of cents, and
// records the subject/namespace it was asked about.
func balanceOf(t *testing.T, cents int64) *[]debit {
	t.Helper()
	prev := object.BalanceReader()
	seen := []debit{}
	object.SetBalanceReader(func(_ context.Context, subject, namespace, currency string) (int64, error) {
		seen = append(seen, debit{subject: subject, namespace: namespace})
		return cents, nil
	})
	t.Cleanup(func() { object.SetBalanceReader(prev) })
	return &seen
}

// TestAnswerIsOneCompletionOneDebit drives the money core of an answer in handler
// order — run the completion, then charge for it — and asserts that ONE request
// moves the model once and the ledger once, for the completion's own price, keyed on
// the answer message.
func TestAnswerIsOneCompletionOneDebit(t *testing.T) {
	got := captureDebits(t)

	const price = 0.00132
	provider := &countingProvider{answer: "42", price: price}

	answer, result, err := object.QueryAnswer(provider, "what is six times seven?", nil, nil, object.AnswerPrompt, "en")
	if err != nil {
		t.Fatalf("QueryAnswer: %v", err)
	}
	if answer != "42" {
		t.Fatalf("answer = %q, want %q", answer, "42")
	}

	answerMessage := &object.Message{
		Owner:         chatOwner,
		Name:          "message_x",
		User:          "alice",
		Author:        "AI",
		Text:          answer,
		ModelProvider: "openai-gpt4",
		TokenCount:    result.TotalTokenCount,
		Price:         model.AddPrices(result.TotalPrice, 0),
		Currency:      result.Currency,
	}
	if err := object.AddTransactionForMessage(answerMessage); err != nil {
		t.Fatalf("AddTransactionForMessage: %v", err)
	}

	if provider.calls != 1 {
		t.Errorf("one request ran the model %d times, want exactly 1", provider.calls)
	}
	if len(*got) != 1 {
		t.Fatalf("one completion must be ONE debit, got %d: %+v", len(*got), *got)
	}
	d := (*got)[0]
	if want := strconv.FormatFloat(price, 'f', 9, 64); d.usd != want {
		t.Errorf("debit usd = %q, want the completion's own price %q", d.usd, want)
	}
	if want := answerMessage.GetId(); d.requestID != want {
		t.Errorf("debit request id = %q, want the answer message id %q", d.requestID, want)
	}
	if d.namespace != chatOwner || d.subject != chatOwner+"/alice" {
		t.Errorf("debit landed on %q/%q, want namespace %q subject %q", d.namespace, d.subject, chatOwner, chatOwner+"/alice")
	}
}

// TestAnswerGateRefusesUnfundedPayer pins the pre-flight half: an answer whose
// estimate exceeds the payer's balance is refused BEFORE the model is asked for a
// real completion. The gate spends one dry run and nothing else.
func TestAnswerGateRefusesUnfundedPayer(t *testing.T) {
	got := captureDebits(t)
	reads := balanceOf(t, 0)

	provider := &countingProvider{answer: "42", price: 1.50}

	err := gateBalance(chatOwner, "alice", "what is six times seven?", nil, object.AnswerPrompt, answerProvider(), provider, "en")
	if err == nil {
		t.Fatal("an unfunded payer must be refused before the model runs, got nil error")
	}
	if !strings.Contains(err.Error(), "insufficient balance") {
		t.Errorf("refusal = %q, want an insufficient-balance refusal", err.Error())
	}

	if provider.dryRuns != 1 {
		t.Errorf("the gate ran %d dry runs, want exactly 1", provider.dryRuns)
	}
	if provider.calls != 0 {
		t.Errorf("a refused answer ran the model %d times, want 0", provider.calls)
	}
	if len(*got) != 0 {
		t.Errorf("a refused answer must charge nothing, got %+v", *got)
	}
	if len(*reads) != 1 || (*reads)[0].subject != chatOwner+"/alice" || (*reads)[0].namespace != chatOwner {
		t.Errorf("gate read %+v, want one read of subject %q in namespace %q", *reads, chatOwner+"/alice", chatOwner)
	}
}

// TestAnswerGateAdmitsFundedPayer is the other side: a funded payer passes the gate,
// and the gate itself charges nothing — the charge belongs to the debit.
func TestAnswerGateAdmitsFundedPayer(t *testing.T) {
	got := captureDebits(t)
	balanceOf(t, 100_00)

	provider := &countingProvider{answer: "42", price: 1.50}
	if err := gateBalance(chatOwner, "alice", "q", nil, object.AnswerPrompt, answerProvider(), provider, "en"); err != nil {
		t.Fatalf("a funded payer must be admitted, got %v", err)
	}
	if len(*got) != 0 {
		t.Errorf("the gate must charge nothing, got %+v", *got)
	}
}

// TestGetAnswerHandlerIsOneCompletionOneDebit pins the money invariant of
// GET /v1/answer on the handler itself.
//
// The handler reads and writes chat/message rows, so it cannot be driven here — but
// the defect was in its SHAPE, not in any callee: it issued the completion TWICE
// (two full upstream calls per request, the second silently overwriting the first)
// and routed the price it computed to no debit at all, so the surface ran a
// client-named model for free. Both halves are exactly a call count, so counting the
// calls in the handler's own syntax tree is the whole test: restore the duplicate
// completion and this sees 2; delete the debit and it sees 0.
func TestGetAnswerHandlerIsOneCompletionOneDebit(t *testing.T) {
	body := handlerBody(t, "message_answer.go", "GetAnswer")

	// Every way this package can reach an upstream completion from a handler.
	if n := countSelectorCalls(body, "object", "QueryAnswer", "GetAnswer", "GetAnswerWithContext"); n != 1 {
		t.Errorf("GetAnswer issues %d completions, want exactly 1 per request", n)
	}
	if n := countSelectorCalls(body, "object", "AddTransactionForMessage"); n != 1 {
		t.Errorf("GetAnswer takes %d debits, want exactly 1 for the completion it ran", n)
	}
	if n := countIdentCalls(body, "gateBalance"); n != 1 {
		t.Errorf("GetAnswer runs the pre-flight balance gate %d times, want exactly 1", n)
	}
}

// TestGetMessageAnswerHandlerIsOneCompletionOneDebit holds the same invariant for
// the chat-answer surface, so a later edit cannot reintroduce there what F3 removed
// here. Its completion goes through the model provider directly (streaming), so the
// count that matters is its single debit and its single pre-flight gate.
func TestGetMessageAnswerHandlerIsOneCompletionOneDebit(t *testing.T) {
	body := handlerBody(t, "message_answer.go", "GetMessageAnswer")

	if n := countSelectorCalls(body, "object", "AddTransactionForMessage"); n != 1 {
		t.Errorf("GetMessageAnswer takes %d debits, want exactly 1", n)
	}
	if n := countIdentCalls(body, "validateTransactionBeforeAIGeneration"); n != 1 {
		t.Errorf("GetMessageAnswer runs the pre-flight balance gate %d times, want exactly 1", n)
	}
}

// TestGetMessageAnswerClaimsBeforeItGenerates pins the wiring of the atomic claim
// into the handler. object.ClaimMessageAnswer is what makes one message one
// completion and one debit under concurrency (its own tests prove that), but only
// if the handler takes it, and takes it BEFORE it spends anything: the pre-flight
// gate's dry run, the embedding behind GetNearestKnowledge and the completion itself
// all cost, and all of them come after.
//
// Unwire the claim and this sees 0. Move it below the gate and the ordering check
// fails.
func TestGetMessageAnswerClaimsBeforeItGenerates(t *testing.T) {
	body := handlerBody(t, "message_answer.go", "GetMessageAnswer")

	claim := firstCallPos(body, "object", "ClaimMessageAnswer")
	if claim == token.NoPos {
		t.Fatal("GetMessageAnswer never claims the message; two concurrent requests both generate and both charge")
	}
	if n := countSelectorCalls(body, "object", "ReleaseMessageAnswer"); n != 1 {
		t.Errorf("GetMessageAnswer releases the claim %d times, want exactly 1 (a failed generation must be retryable before the lease runs out)", n)
	}

	for _, spend := range []struct{ what, pkg, fn string }{
		{"the pre-flight dry run", "", "validateTransactionBeforeAIGeneration"},
		{"the knowledge embedding", "object", "GetNearestKnowledge"},
		{"the debit", "object", "AddTransactionForMessage"},
	} {
		var at token.Pos
		if spend.pkg == "" {
			at = firstIdentCallPos(body, spend.fn)
		} else {
			at = firstCallPos(body, spend.pkg, spend.fn)
		}
		if at != token.NoPos && at < claim {
			t.Errorf("GetMessageAnswer reaches %s at pos %d, before claiming the message at pos %d", spend.what, at, claim)
		}
	}
}

// firstCallPos reports the position of the first pkg.Name(...) call.
func firstCallPos(n ast.Node, pkg, name string) token.Pos {
	found := token.NoPos
	ast.Inspect(n, func(node ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == pkg {
			found = call.Pos()
			return false
		}
		return true
	})
	return found
}

// firstIdentCallPos reports the position of the first call to a package-local func.
func firstIdentCallPos(n ast.Node, name string) token.Pos {
	found := token.NoPos
	ast.Inspect(n, func(node ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			found = call.Pos()
			return false
		}
		return true
	})
	return found
}

// handlerBody returns the body of a named function in a package source file.
func handlerBody(t *testing.T, file, name string) *ast.BlockStmt {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name && fn.Body != nil {
			return fn.Body
		}
	}
	t.Fatalf("%s: no func %s", file, name)
	return nil
}

// countSelectorCalls counts calls to pkg.Name(...) for any of the given names.
func countSelectorCalls(n ast.Node, pkg string, names ...string) int {
	count := 0
	ast.Inspect(n, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != pkg {
			return true
		}
		for _, name := range names {
			if sel.Sel.Name == name {
				count++
			}
		}
		return true
	})
	return count
}

// countIdentCalls counts calls to a package-local function by name.
func countIdentCalls(n ast.Node, name string) int {
	count := 0
	ast.Inspect(n, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			count++
		}
		return true
	})
	return count
}

// nano is a *int64 literal — the shape "this caller knows its exact amount" takes.
func nano(v int64) *int64 { return &v }

// TestBilledAmountHasOneSource pins the money view of a call to a single number.
//
// A caller that knows what it billed says so (BilledNanoExact), and three places
// report that call: the debit, the warehouse row and the o11y span. They used to
// read it two ways — the debit and the span's billed_cost recomputed it from the
// cent-precision rate table while the margin beside them used the exact value — so a
// model with no configured price billed at the conservative $1/$4 default and the
// span reported several times what the invoice charged.
func TestBilledAmountHasOneSource(t *testing.T) {
	// A completion whose real price is 0.00132 USD, on a model the table has no
	// price for — so the table leg would invent the conservative default.
	const price = 0.00132
	rec := &usageRecord{
		Model:            "a-model-with-no-configured-price",
		PromptTokens:     100_000,
		CompletionTokens: 100_000,
		Status:           "success",
		BilledNanoExact:  nano(usdToNano(price)),
	}

	table := usageBilledNano(rec, usageCostNano(rec))
	if table == usdToNano(price) {
		t.Skip("the table happens to agree here; this test needs an unpriced model to be meaningful")
	}

	if got, want := usageMargin(rec).BilledNano, usdToNano(price); got != want {
		t.Errorf("warehouse billed_nano = %d, want the exact %d", got, want)
	}
	if got, want := usageBilledUSD(rec), nanoToUSD(usdToNano(price)); got != want {
		t.Errorf("the debit charges %q, want the exact %q (the table would charge %q)", got, want, nanoToUSD(table))
	}
}

// TestExactZeroIsAnAmountNotAnAbsence pins exactness as PRESENCE. A turn that billed
// exactly nothing knows its amount as precisely as any other; read as "unset" it went
// back to the table, which invents a price for a call that had none.
func TestExactZeroIsAnAmountNotAnAbsence(t *testing.T) {
	free := &usageRecord{
		Model:            "a-model-with-no-configured-price",
		PromptTokens:     100_000,
		CompletionTokens: 100_000,
		Status:           "success",
		BilledNanoExact:  nano(0),
	}

	if got := usageMargin(free).BilledNano; got != 0 {
		t.Errorf("a turn billed exactly nothing reports %d nano, want 0", got)
	}
	if got := usageBilledUSD(free); got != "0" {
		t.Errorf("a turn billed exactly nothing debits %q, want \"0\"", got)
	}
}

// TestUnsetExactnessStillUsesTheTable is the other side: a record that names no exact
// amount resolves exactly as it always did.
func TestUnsetExactnessStillUsesTheTable(t *testing.T) {
	rec := &usageRecord{Model: "gpt-4", PromptTokens: 1000, CompletionTokens: 1000, Status: "success"}
	if got, want := usageMargin(rec).BilledNano, usageBilledNano(rec, usageCostNano(rec)); got != want {
		t.Errorf("an unstamped record bills %d, want the unchanged table value %d", got, want)
	}
}

// TestSpanReportsWhatTheInvoiceCharged pins the o11y span's billed_cost to the same
// number the ledger took. The span recomputed it from the cent-precision rate table
// while the margin beside it used the exact value, so one span carried two different
// billed amounts — and on a model with no configured price the table's is the
// conservative $1/$4 default, several times the invoice.
func TestSpanReportsWhatTheInvoiceCharged(t *testing.T) {
	const price = 0.00132
	rec := &usageRecord{
		Model:            "a-model-with-no-configured-price",
		PromptTokens:     100_000,
		CompletionTokens: 100_000,
		Status:           "success",
		BilledNanoExact:  nano(usdToNano(price)),
	}

	money := spanMoney(rec)
	if money.billed != price {
		t.Errorf("span billed_cost = %v, want the charged %v", money.billed, price)
	}
	// The debit, the warehouse row and the span: one number.
	if got, want := usageBilledUSD(rec), nanoToUSD(usdToNano(price)); got != want {
		t.Errorf("the debit charges %q while the span reports %v", got, money.billed)
	}
	if got := float64(usageMargin(rec).BilledNano) / 1e9; got != money.billed {
		t.Errorf("warehouse billed_nano = %v, span billed_cost = %v; one call, one number", got, money.billed)
	}
	// Margin must be derived from the SAME billed figure, not a second one.
	if got, want := money.margin, money.billed-money.provider; got != want {
		t.Errorf("span margin = %v, want billed − provider = %v", got, want)
	}
}

// TestSpanReportsNothingForAFreeTurn: a turn that billed exactly nothing must not
// appear on the span as having cost the caller anything.
func TestSpanReportsNothingForAFreeTurn(t *testing.T) {
	free := &usageRecord{
		Model:            "a-model-with-no-configured-price",
		PromptTokens:     100_000,
		CompletionTokens: 100_000,
		Status:           "success",
		BilledNanoExact:  nano(0),
	}
	if got := spanMoney(free).billed; got != 0 {
		t.Errorf("span billed_cost = %v for a turn that billed nothing, want 0", got)
	}
}
