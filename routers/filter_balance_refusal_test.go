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

package routers

import (
	stdcontext "context"
	"encoding/json"
	"net/http"
	"sort"
	"testing"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
)

const thirdParty = "anthropic/claude-opus-5.5"

// keysOf is the names of the fields a refusal's error object carries.
func keysOf(t *testing.T, body string) []string {
	t.Helper()
	var r struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("refusal is not JSON: %s", body)
	}
	keys := make([]string, 0, len(r.Error))
	for k := range r.Error {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Every 402 says what limited the call and where funds are added, in the body and on
// the wire, whichever rule refused it: the wallet that cannot cover it, or the policy
// that says nothing pays for it. A program routes on the code and the address; neither
// is read out of a sentence.
func TestEveryPaymentRequiredNamesTheTopUp(t *testing.T) {
	pay := object.PayURL("", "acme")
	for name, run := range map[string]func(t *testing.T) probe{
		"the wallet": func(t *testing.T) probe {
			gateWith(t, 0)
			return chatWith(thirdParty, "/v1/chat/completions")
		},
		"the policy, nothing pays": func(t *testing.T) probe {
			gateWith(t, 0)
			policy(t, nil, &object.LimitHit{Code: object.CodeInsufficientBalance, Class: object.ClassPremium, Model: thirdParty})
			return chatWith(thirdParty, "/v1/chat/completions")
		},
		"the policy, the old code": func(t *testing.T) probe {
			gateWith(t, 0)
			policy(t, nil, &object.LimitHit{Code: object.CodePaidPlan, Class: object.ClassPremium, Model: thirdParty})
			return chatWith(thirdParty, "/v1/responses")
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := run(t)
			if p.status() != http.StatusPaymentRequired {
				t.Fatalf("%d (%s), want 402", p.status(), p.said())
			}
			r := refusalOf(t, p.said())
			if p.replied("X-Hanzo-Usage-Reason") != r.Error.Code || p.replied("X-Hanzo-Usage") != "limited" || p.replied("X-Hanzo-Topup-Url") != pay {
				t.Errorf("headers reason %q usage %q topup %q, want the code %q, limited and %q",
					p.replied("X-Hanzo-Usage-Reason"), p.replied("X-Hanzo-Usage"), p.replied("X-Hanzo-Topup-Url"), r.Error.Code, pay)
			}
			var body struct {
				Error object.BillingError `json:"error"`
			}
			if err := json.Unmarshal([]byte(p.said()), &body); err != nil {
				t.Fatal(err)
			}
			e := body.Error
			if e.Type != "billing_error" || e.TopupURL != pay || e.Class != object.ClassPremium || e.Model != thirdParty {
				t.Errorf("body %+v, want billing_error naming %q, the class and the model", e, pay)
			}
			if len(e.Actions) == 0 || e.Actions[0].Kind != "topup" || e.Actions[0].URL != pay {
				t.Errorf("actions %+v, want the top-up first", e.Actions)
			}
		})
	}
}

// The wallet's refusal and the policy's are one object: the same fields, so a client
// that reads the one reads the other.
func TestTheWalletAndThePolicyRefuseInOneShape(t *testing.T) {
	gateWith(t, 0)
	wallet := chatWith(thirdParty, "/v1/chat/completions")
	policy(t, nil, &object.LimitHit{Code: object.CodeInsufficientBalance, Class: object.ClassPremium, Model: thirdParty})
	rule := chatWith(thirdParty, "/v1/chat/completions")
	a, b := keysOf(t, wallet.said()), keysOf(t, rule.said())
	if len(a) != len(b) {
		t.Fatalf("the wallet says %v, the policy says %v", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("the wallet says %v, the policy says %v", a, b)
		}
	}
}

// A balance that could not be read is no request for funds: 503, retryable, with no
// top-up address and no top-up header.
func TestAnUnreadableBalanceNamesNoTopUp(t *testing.T) {
	n := object.BalanceUnavailable()
	if n.Headers() != nil || len(n.ErrorJSON()) == 0 {
		t.Fatalf("headers %v", n.Headers())
	}
	if got := keysOf(t, string(n.ErrorJSON())); len(got) != 3 {
		t.Errorf("fields %v, want message, type and code alone", got)
	}
}

// The handover is the host's switch. With none installed a signed-in app's free plan
// goes on in Enso, as it always has. With the host refusing the class, the same call
// is the 402 that names the top-up; a class the host does not refuse still hands over.
func TestTheHandoverSwitchChoosesBetweenEnsoAndTheTopUp(t *testing.T) {
	freeRoute(t)
	gateWith(t, 0)
	freeModels(t, controllers.FreeModel)
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		if !q.Priced {
			return nil, nil, nil
		}
		return nil, &object.LimitHit{Code: object.CodeInsufficientBalance, Class: q.Class, Model: q.Model}, nil
	})
	t.Cleanup(func() { object.SetHandover(nil) })
	inApp := func(model string) probe {
		return ask(http.MethodPost, "/v1/chat/completions").with("Authorization", inApp(t)).
			body([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)).through(BalanceGateFilter)
	}

	if p := inApp(thirdParty); p.status() != http.StatusOK || p.replied("X-Hanzo-Fallback") != controllers.FreeModel {
		t.Fatalf("no switch: %d fallback %q (%s), want the handover", p.status(), p.replied("X-Hanzo-Fallback"), p.said())
	}

	var asked []string
	object.SetHandover(func(_ stdcontext.Context, ns, class string) bool {
		asked = append(asked, ns+"/"+class)
		return class != object.ClassPremium
	})
	p := inApp(thirdParty)
	if p.status() != http.StatusPaymentRequired || p.replied("X-Hanzo-Fallback") != "" || refusalOf(t, p.said()).Error.Code != object.CodeInsufficientBalance ||
		p.replied("X-Hanzo-Topup-Url") == "" {
		t.Fatalf("switch off for premium: %d fallback %q (%s), want 402 insufficient_balance naming the top-up", p.status(), p.replied("X-Hanzo-Fallback"), p.said())
	}
	if len(asked) != 1 || asked[0] != "acme/premium" {
		t.Errorf("the switch was asked %v, want once, for acme and the premium class", asked)
	}
}
