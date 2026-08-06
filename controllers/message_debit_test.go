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
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
	web "github.com/hanzoai/ai/web"
)

// debit is one charge the ai module made against the host's money hook.
type debit struct {
	subject   string
	namespace string
	usd       string
	requestID string
}

// captureDebits installs a counting object.UsageRecorder — the SAME hook the cloud
// host installs, and the ONE place every ai-module charge lands. Any second charge
// for a single completion, from ANY path, shows up here as a second element.
func captureDebits(t *testing.T) *[]debit {
	t.Helper()
	prev := object.UsageRecorder()
	var mu sync.Mutex
	got := []debit{}
	object.SetUsageRecorder(func(_ context.Context, u object.UsageEvent) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, debit{subject: u.Subject, namespace: u.Namespace, usd: u.USD, requestID: u.RequestID})
		return nil
	})
	t.Cleanup(func() { object.SetUsageRecorder(prev) })
	return &got
}

// newAnswerController builds the controller the answer handlers run on, with a live
// request so the trace path can read its remote address and context.
func newAnswerController() *ApiController {
	req := httptest.NewRequest("GET", "/v1/get-message-answer?id=acme/msg-1", nil)
	ctx := web.NewContext()
	ctx.Reset(httptest.NewRecorder(), req)
	c := &ApiController{}
	c.Init(ctx, "ApiController", "GetMessageAnswer", nil)
	return c
}

// TestCasibaseChatAnswerIsOneDebit pins the money invariant of a casibase chat
// answer: one completion, one debit.
//
// GetMessageAnswer meters the turn at two points — the trace
// (recordCasibaseChatUsage) and the charge (AddTransactionForMessage) — and both
// reach object.UsageRecorder. This drives that exact pair in handler order and
// asserts a SINGLE charge, of the completion's own TotalPrice, keyed on the message
// id (the idempotency key a retry can dedupe). Restoring the recordUsage call in
// recordCasibaseChatUsage makes this see 2 debits and fail.
func TestCasibaseChatAnswerIsOneDebit(t *testing.T) {
	got := captureDebits(t)

	const price = 0.00132
	chat := &object.Chat{Owner: "acme", Organization: "acme", User: "alice"}
	provider := &object.Provider{Name: "openai-gpt4"}
	result := &model.ModelResult{
		PromptTokenCount:   100,
		ResponseTokenCount: 200,
		TotalTokenCount:    300,
		TotalPrice:         price,
		Currency:           "USD",
	}
	message := &object.Message{
		Owner:         "acme",
		Name:          "msg-1",
		User:          "alice",
		Price:         price,
		Currency:      "USD",
		ModelProvider: "openai-gpt4",
	}

	c := newAnswerController()
	// The two meter points of one casibase answer, in handler order
	// (message_answer.go: recordCasibaseChatUsage, then AddTransactionForMessage).
	c.recordCasibaseChatUsage(chat, provider, result)
	if err := object.AddTransactionForMessage(message); err != nil {
		t.Fatalf("AddTransactionForMessage: %v", err)
	}

	if len(*got) != 1 {
		t.Fatalf("one completion must be ONE debit, got %d: %+v", len(*got), *got)
	}
	d := (*got)[0]
	if want := strconv.FormatFloat(price, 'f', 9, 64); d.usd != want {
		t.Errorf("debit usd = %q, want the completion's TotalPrice %q", d.usd, want)
	}
	if want := message.GetId(); d.requestID != want {
		t.Errorf("debit request id = %q, want the message id %q (the dedupe key)", d.requestID, want)
	}
	if d.namespace != "acme" || d.subject != "acme/alice" {
		t.Errorf("debit landed on %q/%q, want namespace acme subject acme/alice", d.namespace, d.subject)
	}
}

// TestCasibaseChatTraceDoesNotCharge isolates the trace half: on its own,
// recordCasibaseChatUsage must move no money. It reports the turn to the warehouse
// and o11y only — the charge belongs to AddTransactionForMessage.
func TestCasibaseChatTraceDoesNotCharge(t *testing.T) {
	got := captureDebits(t)

	c := newAnswerController()
	c.recordCasibaseChatUsage(
		&object.Chat{Owner: "acme", Organization: "acme", User: "alice"},
		&object.Provider{Name: "openai-gpt4"},
		&model.ModelResult{PromptTokenCount: 100, ResponseTokenCount: 200, TotalTokenCount: 300, TotalPrice: 0.00132, Currency: "USD"},
	)

	if len(*got) != 0 {
		t.Fatalf("the trace must charge nothing, got %d debit(s): %+v", len(*got), *got)
	}
}

// TestMessageAnswerIsBilledOnce covers every NON-casibase message answer: those
// meter only through AddTransactionForMessage, which must still charge exactly once,
// for the message's own price. Guards against fixing the casibase double-charge by
// disarming the general per-answer debit.
func TestMessageAnswerIsBilledOnce(t *testing.T) {
	got := captureDebits(t)

	const price = 0.25
	message := &object.Message{
		Owner:         "acme",
		Name:          "msg-2",
		User:          "bob",
		Price:         price,
		Currency:      "USD",
		ModelProvider: "anthropic-opus",
	}
	if err := object.AddTransactionForMessage(message); err != nil {
		t.Fatalf("AddTransactionForMessage: %v", err)
	}

	if len(*got) != 1 {
		t.Fatalf("a message answer must be billed exactly once, got %d: %+v", len(*got), *got)
	}
	if want := strconv.FormatFloat(price, 'f', 9, 64); (*got)[0].usd != want {
		t.Errorf("debit usd = %q, want %q", (*got)[0].usd, want)
	}
}

// TestCasibaseChatTraceCarriesExactBilledAmount pins warehouse/invoice
// reconciliation: the traced row reports the dollars the ledger actually took
// (BilledNanoExact), not a rate-table recompute keyed on a provider name — which for
// an unpriced name would invent the conservative default and disagree with the
// invoice.
func TestCasibaseChatTraceCarriesExactBilledAmount(t *testing.T) {
	rec := &usageRecord{
		Model:            "openai-gpt4",
		PromptTokens:     100,
		CompletionTokens: 200,
		BilledNanoExact:  usdToNano(0.00132),
	}
	if got, want := usageMargin(rec).BilledNano, int64(1_320_000); got != want {
		t.Fatalf("traced billed_nano = %d, want the charged %d", got, want)
	}
}
