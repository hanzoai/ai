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
	"strings"
)

// payBaseURL is the hosted prepaid-wallet page a spend-gate denial points the caller
// to. The pay SPA is tenant-neutral: it resolves the BRAND from the request Host and
// the WALLET from the authenticated IAM identity (the `owner` claim), so signing in
// lands the caller on their own org-pooled wallet. Hanzo serves it at hanzo.ai/pay,
// so a person pays without leaving the site; pay.hanzo.ai redirects there with its
// path and query. It has no /<org> route today (pay/src/main.tsx routes: / /amount
// /confirm /success), so PayURL returns this bare root; see PayURL for the per-org
// deep-link seam.
const payBaseURL = "https://hanzo.ai/pay"

// payHosts maps a request host to the wallet that serves ITS brand. One cloud origin
// answers every brand and reads the brand off the Host, so a denial built without the
// Host can only ever name one brand's wallet -- which is what sent a Lux caller to
// Hanzo's page while pay.lux.cloud was live and white-labelled by hostname.
//
// A brand absent here has no wallet of its own and falls back to payBaseURL: pars has
// no pay host at any of its domains, and naming one that does not resolve would be a
// worse answer than naming somebody else's that does. Suffix-matched so a subdomain
// resolves with its parent, and longest match is not needed because no suffix here is
// a suffix of another.
var payHosts = []struct{ suffix, url string }{
	{"lux.cloud", "https://pay.lux.cloud"},
	{"lux.network", "https://pay.lux.cloud"},
	{"lux.id", "https://pay.lux.cloud"},
	{"zoo.cloud", "https://pay.zoo.cloud"},
	{"zoo.ngo", "https://pay.zoo.cloud"},
	{"zoo.network", "https://pay.zoo.cloud"},
	{"zoolabs.id", "https://pay.zoo.cloud"},
}

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
	// CodeUsageUnavailable: the usage limits or the free allowance could NOT be read.
	// Denied — a gate that cannot decide does not admit — and retryable. HTTP 503.
	CodeUsageUnavailable = "usage_unavailable"
)

// BillingNotice is the ONE description of a spend-gate denial: the caller-facing
// Message (with the wallet link embedded where adding credits is the remedy), the
// machine Code clients switch on, and the HTTP Status. Every 402/503 the gateway emits
// for a spend gate — the router balance filter and every controller reservation gate —
// is built here, so the copy, the code, and the pay link live in exactly one place. The
// per-surface response ENCODING (the Anthropic error body, the cloud ZAP response, the
// casibase error envelope) stays at each call site; the OpenAI-dialect surfaces all
// write ErrorJSON, the one body (BillingError) a limit's refusal writes too.
type BillingNotice struct {
	Message string
	Code    string
	Status  int
	// Pay is the wallet page that adds funds, "" where adding funds is not the remedy
	// (a lookup that failed). With it the refusal names the page in the body
	// (topup_url, the topup action) and, on the wire, in X-Hanzo-Topup-Url.
	Pay string
	// Model and Class say what was refused: the model asked for and the class the
	// usage policy puts it in. Either may be empty for a call that names no model.
	Model, Class string
}

// For names the model a refusal is about and its class, which the body carries so a
// picker can say which model needs funds.
func (n BillingNotice) For(model, class string) BillingNotice {
	n.Model, n.Class = model, class
	return n
}

// BillingAction is one way on from a refusal: the page that adds funds (topup), the
// plan that raises the limit (upgrade), the wait until it reopens (wait), the choice
// to continue with granted credit (credits), or the Hanzo model to ask instead
// (switch).
type BillingAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
	Plan  string `json:"plan,omitempty"`
	Model string `json:"model,omitempty"`
}

// BillingError is the ONE error object every billing refusal carries, whichever gate
// refused: a wallet that cannot cover the call, a plan that has not room for it, a
// model nothing pays for. Clients switch on Code and read TopupURL and Actions to say
// where to add funds; they never read Message for either.
type BillingError struct {
	Message    string          `json:"message"`
	Type       string          `json:"type"`
	Code       string          `json:"code"`
	Class      string          `json:"class,omitempty"`
	Model      string          `json:"model,omitempty"`
	Fallback   string          `json:"fallback,omitempty"`
	Limit      string          `json:"limit,omitempty"`
	ResetsAt   string          `json:"resets_at,omitempty"`
	TopupURL   string          `json:"topup_url,omitempty"`
	UpgradeURL string          `json:"upgrade_url,omitempty"`
	Actions    []BillingAction `json:"actions,omitempty"`
}

// TopupAction is the action that adds funds, at the wallet page pay.
func TopupAction(pay string) BillingAction {
	return BillingAction{Kind: "topup", Label: "Buy usage credits", URL: pay}
}

// InsufficientBalance is the denial for a KNOWN balance that cannot cover the request —
// genuine insufficiency, so the caller is told to add credits. noun names the priced
// quantity ("request cost", "image cost", "video cost", "cost"); "" yields the plain
// form the router balance filter uses. org is the billing owner the gate resolved,
// carried for the wallet link (see PayURL).
func InsufficientBalance(host, org, noun string) BillingNotice {
	msg := "Insufficient balance"
	if noun != "" {
		msg += " for the estimated " + noun
	}
	pay := PayURL(host, org)
	msg += ". Add credits to your wallet at " + pay
	return BillingNotice{Message: msg, Code: CodeInsufficientBalance, Status: http.StatusPaymentRequired, Pay: pay}
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

// UsageUnavailable is the denial for usage limits or a free allowance that could NOT
// be read: fail-closed, retryable, and no wallet link — paying is not the remedy.
func UsageUnavailable() BillingNotice {
	return BillingNotice{
		Message: "Unable to read your usage limits right now. Please retry in a moment.",
		Code:    CodeUsageUnavailable,
		Status:  http.StatusServiceUnavailable,
	}
}

// PayURL returns the hosted prepaid-wallet link for the billing org that owns a denied
// request. The pay SPA selects the caller's org-pooled wallet from the authenticated
// identity — it has no /<org> route yet (see payBaseURL) — so the link is the bare
// wallet root and the caller lands on their own wallet on sign-in. org is threaded from
// every denial site so a pay.<brand>/<org> deep-link, once the pay SPA serves one, is a
// one-line change here (return payBaseURL + "/" + url.PathEscape(org)).
func PayURL(host, org string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i] // strip :port
	}
	for _, e := range payHosts {
		if h == e.suffix || strings.HasSuffix(h, "."+e.suffix) {
			return e.url
		}
	}
	return payBaseURL
}

// ErrorJSON renders the notice as {"error":{"message":..,"type":"billing_error","code":..}}
// and, where adding funds is the remedy, the model, class, topup_url and topup action:
// the same object a limit's refusal writes (BillingError). The wire "type" is always
// billing_error (both codes are billing errors); clients switch on "code". It is
// marshaled (not string-concatenated) so the message is always correctly JSON-escaped.
func (n BillingNotice) ErrorJSON() []byte {
	e := BillingError{Message: n.Message, Type: "billing_error", Code: n.Code, Class: n.Class, Model: n.Model}
	if n.Pay != "" {
		e.TopupURL = n.Pay
		e.Actions = []BillingAction{TopupAction(n.Pay)}
	}
	b, _ := json.Marshal(struct {
		Error BillingError `json:"error"`
	}{e})
	return b
}

// Headers are the response headers a 402 carries beside its body, so a client that
// reads headers (a streaming relay, an app) routes on them as a client that reads the
// body does: the usage is limited, the code that limited it, the class of the model,
// and where funds are added. Empty for a notice that is not a 402.
func (n BillingNotice) Headers() map[string]string {
	if n.Status != http.StatusPaymentRequired {
		return nil
	}
	return PaymentHeaders(n.Code, n.Class, n.Pay)
}

// PaymentHeaders are the headers every 402 carries: X-Hanzo-Usage and
// X-Hanzo-Usage-Reason (the code), X-Hanzo-Usage-Class where the class is known and
// X-Hanzo-Topup-Url where funds are added.
func PaymentHeaders(code, class, pay string) map[string]string {
	h := map[string]string{"X-Hanzo-Usage": "limited", "X-Hanzo-Usage-Reason": code}
	if class != "" {
		h["X-Hanzo-Usage-Class"] = class
	}
	if pay != "" {
		h["X-Hanzo-Topup-Url"] = pay
	}
	return h
}
