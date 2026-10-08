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
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
)

// policy installs a host that answers every ask with grant and hit, and records the
// asks.
func policy(t *testing.T, grant *object.LimitGrant, hit *object.LimitHit) *[]object.LimitAsk {
	t.Helper()
	var asked []object.LimitAsk
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked = append(asked, q)
		return grant, hit, nil
	})
	return &asked
}

// The plan's included usage pays for a premium model: an empty wallet admits it, and
// the response says the plan paid and where the class stands.
func TestThePlanPaysForAPremiumModelAtAnEmptyWallet(t *testing.T) {
	gateWith(t, 0)
	policy(t, &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Settle: func(int64) {}}, nil)
	p := chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions")
	if p.status() != http.StatusOK {
		t.Fatalf("premium under the plan at $0: %d (%s)", p.status(), p.said())
	}
	if p.replied("X-Hanzo-Paid-By") != object.PaysPlan || p.replied("X-Hanzo-Usage-Class") != object.ClassPremium || p.replied("X-Hanzo-Usage") != "ok" {
		t.Errorf("usage headers paid-by %q class %q state %q", p.replied("X-Hanzo-Paid-By"), p.replied("X-Hanzo-Usage-Class"), p.replied("X-Hanzo-Usage"))
	}
}

// Past the plan's allowance prepaid pays: the request goes on to the wallet, which
// admits a funded caller and refuses an empty one.
func TestPrepaidPaysPastTheAllowanceThroughTheWallet(t *testing.T) {
	g := &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPrepaid, Class: object.ClassOurs, State: "limited", Cash: true}
	gateWith(t, 500)
	policy(t, g, nil)
	p := chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions")
	// To a customer prepaid and granted are both credits.
	if p.status() != http.StatusOK || p.replied("X-Hanzo-Paid-By") != object.PaysCredits {
		t.Fatalf("prepaid past the allowance: %d paid-by %q (%s)", p.status(), p.replied("X-Hanzo-Paid-By"), p.said())
	}

	gateWith(t, 0)
	policy(t, g, nil)
	if p := chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions"); p.status() != http.StatusPaymentRequired {
		t.Fatalf("prepaid with an empty wallet: %d, want the wallet's 402 (%s)", p.status(), p.said())
	}
}

// Limited mode: a conversation the plan can no longer pay for is answered by the free
// model, saying so — from any client, since limited free usage past the included usage
// is part of every paid plan. Outside chat no free model answers, and the refusal is
// 402 plan_allowance_used, naming the class and the ways on, and no figure.
func TestLimitedModeFallsBackOrRefusesWithoutAFigure(t *testing.T) {
	gateWith(t, 0)
	freeModels(t, controllers.FreeModel)
	var asked []object.LimitAsk
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked = append(asked, q)
		if q.Model == controllers.FreeModel {
			return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassFree, State: "limited", Settle: func(int64) {}}, nil, nil
		}
		return nil, &object.LimitHit{Code: object.CodePlanAllowance, Class: object.ClassPremium, Upgrade: "max-20x",
			Message: "Your plan's included usage for premium models is used for now. Add prepaid credit or upgrade:"}, nil
	})

	p := ask(http.MethodPost, "/v1/chat/completions").with("Authorization", "Bearer tok").with("X-Hanzo-Fallback", "allow").
		body([]byte(`{"model":"anthropic/claude-opus-5.5","messages":[{"role":"user","content":"hi"}]}`)).through(BalanceGateFilter)
	if p.status() != http.StatusOK || p.replied("X-Hanzo-Fallback") != controllers.FreeModel || p.replied("X-Hanzo-Usage-Reason") != object.CodePlanAllowance {
		t.Fatalf("limited mode with fallback: %d fallback %q reason %q (%s)", p.status(), p.replied("X-Hanzo-Fallback"), p.replied("X-Hanzo-Usage-Reason"), p.said())
	}
	if !strings.Contains(p.handed(), `"model":"`+controllers.FreeModel+`"`) {
		t.Errorf("the handler was handed %s, want the free model", p.handed())
	}
	if len(asked) != 2 || asked[1].Model != controllers.FreeModel || asked[1].Priced {
		t.Errorf("the free model was not asked of the plan's windows: %+v", asked)
	}

	asked = nil
	p = chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions")
	if p.status() != http.StatusOK || p.replied("X-Hanzo-Fallback") != controllers.FreeModel || p.replied("X-Hanzo-Usage") != "limited" ||
		!strings.Contains(p.handed(), `"model":"`+controllers.FreeModel+`"`) {
		t.Fatalf("a program past its plan's allowance: %d fallback %q usage %q (%s), want limited mode", p.status(),
			p.replied("X-Hanzo-Fallback"), p.replied("X-Hanzo-Usage"), p.said())
	}

	p = chatWith("anthropic/claude-opus-5.5", "/v1/embeddings")
	if p.status() != http.StatusPaymentRequired {
		t.Fatalf("past the allowance outside chat: %d (%s)", p.status(), p.said())
	}
	r := refusalOf(t, p.said())
	if r.Error.Code != object.CodePlanAllowance || !strings.Contains(p.said(), `"class":"premium"`) || !strings.Contains(p.said(), `"kind":"topup"`) {
		t.Errorf("refusal %s", p.said())
	}
	if regexp.MustCompile(`\$\d|\d+ ?(cents|requests)|\d+\.\d\d`).MatchString(r.Error.Message) {
		t.Errorf("the refusal names a figure: %q", r.Error.Message)
	}
}

// A model that needs a paid plan or prepaid balance is refused 402 paid_plan_required
// on /v1/decisions, in the decision service's own wire, before any wallet is read.
func TestADecisionNeedingAPaidPlanIsRefusedInItsOwnWire(t *testing.T) {
	gateWith(t, 0)
	policy(t, nil, &object.LimitHit{Code: object.CodePaidPlan, Class: object.ClassOurs, Message: "Jev needs a paid plan or prepaid balance. Upgrade or add prepaid credit:"})
	p := ask(http.MethodPost, "/v1/decisions").with("Authorization", "Bearer tok").
		body([]byte(`{"model":"typesafe/jev-1.13","state":"s","questions":{"q":{"type":"noul"}}}`)).
		through(Dialect, BalanceGateFilter)
	if p.status() != http.StatusPaymentRequired || !strings.Contains(p.said(), `"code":"paid_plan_required"`) || !strings.Contains(p.said(), "Jev needs a paid plan") {
		t.Fatalf("jev without a paid plan: %d %s", p.status(), p.said())
	}
}

// A decision the plan or a free cap pays for reaches its handler at an empty wallet:
// the empty-wallet refusal waits on the policy's answer.
func TestACoveredDecisionReachesItsHandlerAtAnEmptyWallet(t *testing.T) {
	gateWith(t, 0)
	asked := policy(t, &object.LimitGrant{Plan: "free", Pays: object.PaysFree, Class: object.ClassOurs, State: "ok", Settle: func(int64) {}}, nil)
	p := ask(http.MethodPost, "/v1/decisions").with("Authorization", "Bearer tok").
		body([]byte(`{"model":"kai","state":"s","questions":{"q":{"type":"noul"}}}`)).
		through(Dialect, BalanceGateFilter)
	if p.status() != http.StatusOK || p.replied("X-Hanzo-Paid-By") != object.PaysFree {
		t.Fatalf("a capped free decision at $0: %d paid-by %q (%s)", p.status(), p.replied("X-Hanzo-Paid-By"), p.said())
	}
	if len(*asked) != 1 || (*asked)[0].Model != "kai" || (*asked)[0].Spend {
		t.Fatalf("asked %+v", *asked)
	}
}

// A model that has used its share of the plan hands a conversation to its Hanzo
// fallback, which the policy is asked about in turn, from any client; other premium
// models are untouched. Outside chat the refusal is 402 model_cap, naming the model
// and the fallback, offering the switch, and no figure.
func TestAModelCapHandsAConversationToItsFallback(t *testing.T) {
	gateWith(t, 0)
	freeModels(t, "enso", controllers.FreeModel)
	var asked []object.LimitAsk
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked = append(asked, q)
		switch {
		case strings.HasPrefix(q.Model, "anthropic/claude-opus"):
			return nil, &object.LimitHit{Code: object.CodeModelCap, Class: object.ClassPremium, Model: "anthropic/claude-opus*", Fallback: "enso",
				Upgrade: "max-20x", Message: "Claude Opus has used its share of your plan for now. Try Enso, continue with credits, or upgrade:"}, nil
		case q.Model == "enso":
			return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassFree, State: "ok", Settle: func(int64) {}}, nil, nil
		default:
			return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Settle: func(int64) {}}, nil, nil
		}
	})

	p := ask(http.MethodPost, "/v1/chat/completions").with("Authorization", "Bearer tok").with("X-Hanzo-Fallback", "allow").
		body([]byte(`{"model":"anthropic/claude-opus-4.7","messages":[{"role":"user","content":"hi"}]}`)).through(BalanceGateFilter)
	if p.status() != http.StatusOK || p.replied("X-Hanzo-Fallback") != "enso" || p.replied("X-Hanzo-Usage-Reason") != object.CodeModelCap {
		t.Fatalf("opus past its cap: %d fallback %q reason %q (%s)", p.status(), p.replied("X-Hanzo-Fallback"), p.replied("X-Hanzo-Usage-Reason"), p.said())
	}
	if !strings.Contains(p.handed(), `"model":"enso"`) {
		t.Errorf("the handler was handed %s, want enso", p.handed())
	}
	if len(asked) != 2 || asked[1].Model != "enso" {
		t.Errorf("enso was not asked of the policy: %+v", asked)
	}

	// Another premium model still runs on the plan.
	if p := chatWith("openai/gpt-5.5", "/v1/chat/completions"); p.status() != http.StatusOK || p.replied("X-Hanzo-Paid-By") != object.PaysPlan {
		t.Fatalf("another premium model: %d paid-by %q (%s)", p.status(), p.replied("X-Hanzo-Paid-By"), p.said())
	}

	p = chatWith("anthropic/claude-opus-4.7", "/v1/chat/completions")
	if p.status() != http.StatusOK || p.replied("X-Hanzo-Fallback") != "enso" || !strings.Contains(p.handed(), `"model":"enso"`) {
		t.Fatalf("a program's opus past its cap: %d fallback %q (%s), want enso", p.status(), p.replied("X-Hanzo-Fallback"), p.said())
	}

	p = chatWith("anthropic/claude-opus-4.7", "/v1/embeddings")
	if p.status() != http.StatusPaymentRequired {
		t.Fatalf("opus past its cap outside chat: %d (%s)", p.status(), p.said())
	}
	r := refusalOf(t, p.said())
	if r.Error.Code != object.CodeModelCap || !strings.Contains(p.said(), `"fallback":"enso"`) ||
		!strings.Contains(p.said(), `"kind":"switch"`) || !strings.Contains(p.said(), `"label":"Try Enso"`) || !strings.Contains(p.said(), `"kind":"topup"`) {
		t.Errorf("refusal %s", p.said())
	}
	if regexp.MustCompile(`\$\d|\d+ ?(cents|requests)|\d+\.\d\d`).MatchString(r.Error.Message) {
		t.Errorf("the refusal names a figure: %q", r.Error.Message)
	}
}

// A payer who holds credit and has not chosen to spend it past the plan is offered
// that choice instead of a top-up, where a refusal is given (outside chat).
func TestARefusalOffersCreditsToAPayerWhoHoldsThem(t *testing.T) {
	gateWith(t, 0)
	policy(t, nil, &object.LimitHit{Code: object.CodePlanAllowance, Class: object.ClassPremium, Credits: true,
		Message: "Your plan's included usage of premium models is used for now. Continue with credits or upgrade:"})
	p := chatWith("anthropic/claude-opus-5.5", "/v1/embeddings")
	if p.status() != http.StatusPaymentRequired || !strings.Contains(p.said(), `"kind":"credits"`) || !strings.Contains(p.said(), `"label":"Continue with credits"`) {
		t.Fatalf("credits action: %d %s", p.status(), p.said())
	}
	if strings.Contains(p.said(), `"kind":"topup"`) {
		t.Errorf("a payer with credits was asked to top up: %s", p.said())
	}
}

// A conversation the WALLET cannot pay for goes on in limited mode exactly as one the
// plan cannot: free plan, $0, a premium model in chat — the free model answers, the
// reason is insufficient_balance, and the response names the ways out. A program that
// did not ask still gets its 402.
func TestAnEmptyWalletInChatFallsBackToTheFreeModel(t *testing.T) {
	gateWith(t, 0)
	freeModels(t, controllers.FreeModel)
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return nil, nil, nil // no plan: the wallet decides
	})
	p := ask(http.MethodPost, "/v1/chat/completions").with("Authorization", "Bearer tok").with("X-Hanzo-Fallback", "allow").
		body([]byte(`{"model":"anthropic/claude-haiku-4.5","messages":[{"role":"user","content":"hi"}]}`)).through(BalanceGateFilter)
	if p.status() != http.StatusOK || p.replied("X-Hanzo-Fallback") != controllers.FreeModel ||
		p.replied("X-Hanzo-Usage-Reason") != object.CodeInsufficientBalance || p.replied("X-Hanzo-Usage") != "limited" {
		t.Fatalf("empty wallet in chat: %d fallback %q reason %q usage %q (%s)", p.status(),
			p.replied("X-Hanzo-Fallback"), p.replied("X-Hanzo-Usage-Reason"), p.replied("X-Hanzo-Usage"), p.said())
	}
	if !strings.Contains(p.handed(), `"model":"`+controllers.FreeModel+`"`) {
		t.Errorf("the handler was handed %s, want the free model", p.handed())
	}
	if p.replied("X-Hanzo-Topup-Url") == "" || p.replied("X-Hanzo-Upgrade-Url") == "" {
		t.Errorf("topup %q upgrade %q, want both ways out named", p.replied("X-Hanzo-Topup-Url"), p.replied("X-Hanzo-Upgrade-Url"))
	}

	if p := chatWith("anthropic/claude-haiku-4.5", "/v1/chat/completions"); p.status() != http.StatusPaymentRequired ||
		!strings.Contains(p.said(), object.CodeInsufficientBalance) {
		t.Fatalf("a program that did not ask: %d, want its 402 insufficient_balance (%s)", p.status(), p.said())
	}
	if p := ask(http.MethodPost, "/v1/embeddings").with("Authorization", "Bearer tok").with("X-Hanzo-Fallback", "allow").
		body([]byte(`{"model":"anthropic/claude-haiku-4.5","input":"hi"}`)).through(BalanceGateFilter); p.status() != http.StatusPaymentRequired {
		t.Fatalf("a non-chat call: %d, want its 402 (%s)", p.status(), p.said())
	}
}
