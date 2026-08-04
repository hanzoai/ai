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

package object

import (
	"encoding/json"
	"net/http"
)

// payBaseURL is the hosted prepaid-wallet page a spend-gate denial points the caller
// to. pay.<brand> is a tenant-neutral SPA: it resolves the BRAND from the request Host
// and the WALLET from the authenticated IAM identity (the `owner` claim), so signing in
// lands the caller on their own org-pooled wallet. It has no /<org> route today
// (pay/src/main.tsx routes: / /amount /confirm /success), so PayURL returns this bare
// root; see PayURL for the per-org deep-link seam.
const payBaseURL = "https://pay.hanzo.ai"

// Billing-denial codes. Clients (SDKs, the hanzo CLI) switch on Code, never on the
// human-readable Message.
const (
	// CodeInsufficientBalance: a KNOWN balance cannot cover the request. Actionable —
	// the caller must add credits. HTTP 402.
	CodeInsufficientBalance = "insufficient_balance"
	// CodeBalanceUnavailable: the balance could NOT be verified (a transient billing
	// lookup failure). The request is still denied — fail-CLOSED, a balance we cannot
	// read is never spent — but it is retryable and must NOT tell a funded caller to
	// add credits. HTTP 503.
	CodeBalanceUnavailable = "balance_unavailable"
	// CodePlanRequired: the caller's SUBSCRIPTION does not include the model they
	// asked for (the family SKU ladder: enso-flash free, enso trial+, enso-ultra
	// paid). Distinct from insufficient_balance — credits cannot clear it, only a
	// plan can — so the remedy is the plan page, never the wallet. HTTP 403.
	CodePlanRequired = "plan_required"
)

// BillingNotice is the ONE description of a spend-gate denial: the caller-facing
// Message (with the wallet link embedded where adding credits is the remedy), the
// machine Code clients switch on, and the HTTP Status. Every 402/503 the gateway emits
// for a spend gate — the router balance filter and every controller reservation gate —
// is built here, so the copy, the code, and the pay link live in exactly one place. The
// per-surface response ENCODING (the Anthropic error body, the cloud ZAP response, the
// casibase error envelope, the filter's raw JSON) stays at each call site; only this
// Message+Code+Status is shared.
type BillingNotice struct {
	Message string
	Code    string
	Status  int
}

// InsufficientBalance is the denial for a KNOWN balance that cannot cover the request —
// genuine insufficiency, so the caller is told to add credits. noun names the priced
// quantity ("request cost", "image cost", "video cost", "cost"); "" yields the plain
// form the router balance filter uses. org is the billing owner the gate resolved,
// carried for the wallet link (see PayURL).
func InsufficientBalance(org, noun string) BillingNotice {
	msg := "Insufficient balance"
	if noun != "" {
		msg += " for the estimated " + noun
	}
	msg += ". Add credits to your wallet at " + PayURL(org)
	return BillingNotice{Message: msg, Code: CodeInsufficientBalance, Status: http.StatusPaymentRequired}
}

// BalanceUnavailable is the denial for a balance that could NOT be verified — a
// transient billing-backend lookup failure. It is fail-CLOSED (the request is denied; an
// unreadable balance is never spent) but DISTINCT from insufficiency: a funded caller
// hitting a blip is asked to retry, not to add credits, and gets a retryable 503. It
// carries no wallet link — adding credits is not the remedy for a lookup failure.
func BalanceUnavailable() BillingNotice {
	return BillingNotice{
		Message: "Unable to verify your balance right now. Please retry in a moment.",
		Code:    CodeBalanceUnavailable,
		Status:  http.StatusServiceUnavailable,
	}
}

// planURL is the hosted page where a caller actually CHANGES their plan — the console's
// billing-center Subscriptions tab. It is the plan twin of payBaseURL, and it is a
// different page on purpose: a plan refusal is not cleared by adding credits, so
// pointing it at the wallet would be a paywall with no way through.
const planURL = "https://console.hanzo.ai/billing/subscriptions"

// PlanRequired is the refusal for a model the caller's SUBSCRIPTION does not include —
// the family SKU ladder's subscription floor and its prepaid-funding floor, which refuse
// for the same caller-facing reason. It carries the PLAN link, not the wallet link:
// credits cannot buy a rung on the ladder.
func PlanRequired(model string) BillingNotice {
	return BillingNotice{
		Message: model + " is not included in your plan. Upgrade at " + planURL,
		Code:    CodePlanRequired,
		Status:  http.StatusForbidden,
	}
}

// PayURL returns the hosted prepaid-wallet link for the billing org that owns a denied
// request. The pay SPA selects the caller's org-pooled wallet from the authenticated
// identity — it has no /<org> route yet (see payBaseURL) — so the link is the bare
// wallet root and the caller lands on their own wallet on sign-in. org is threaded from
// every denial site so a pay.<brand>/<org> deep-link, once the pay SPA serves one, is a
// one-line change here (return payBaseURL + "/" + url.PathEscape(org)).
func PayURL(org string) string {
	return payBaseURL
}

// billingErrorEnvelope is the OpenAI-compatible error shape the gateway's raw-HTTP
// surfaces emit. Field order (message, type, code) matches the prior hand-written body,
// so the wire shape is byte-for-byte the same.
type billingErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// ErrorJSON renders the notice as {"error":{"message":..,"type":"billing_error","code":..}},
// the shape the router balance filter writes. The wire "type" is always billing_error
// (both codes are billing errors); clients switch on "code". It is marshaled (not string-
// concatenated) so the message is always correctly JSON-escaped.
func (n BillingNotice) ErrorJSON() []byte {
	var e billingErrorEnvelope
	e.Error.Message = n.Message
	e.Error.Type = "billing_error"
	e.Error.Code = n.Code
	b, _ := json.Marshal(e)
	return b
}
