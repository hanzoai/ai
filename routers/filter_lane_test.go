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

package routers

import (
	"bytes"
	stdcontext "context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// paidSwitch turns the platform's paid lane on for one test.
func paidSwitch(t *testing.T) {
	t.Helper()
	saved := controllers.FreeOnly
	controllers.FreeOnly = func() bool { return false }
	t.Cleanup(func() { controllers.FreeOnly = saved })
}

// chatThrough sends a chat for model through the gate and the lane filter.
func chatThrough(model string) probe {
	return ask(http.MethodPost, "/v1/chat/completions").
		with("Authorization", "Bearer tok").
		body([]byte(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)).
		through(BalanceGateFilter, LaneFilter)
}

// THE LANE FOLLOWS THE GATE'S GRANT. A subscriber whose plan pays, and a payer whose
// own prepaid or granted credit pays, plan or none, are on the paid lane and told so;
// a free model and a caller the policy said nothing about are on the free lane. A
// path no policy governs carries no lane.
func TestTheLaneFollowsTheGatesGrant(t *testing.T) {
	paidSwitch(t)
	for _, tc := range []struct {
		name  string
		grant *object.LimitGrant
		lane  string
	}{
		{"a subscriber whose plan pays", &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Spend: 5_000_000_000, Settle: func(int64) {}}, "paid"},
		{"a subscriber whose prepaid pays past the plan", &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPrepaid, Class: object.ClassPremium, State: "near"}, "paid"},
		{"a wallet with no plan", &object.LimitGrant{Pays: object.PaysPrepaid, Class: object.ClassPremium, State: "ok"}, "paid"},
		{"a wallet on the free plan", &object.LimitGrant{Plan: "free", Pays: object.PaysPrepaid, Class: object.ClassPremium, State: "ok"}, "paid"},
		{"granted credit with no plan", &object.LimitGrant{Plan: "free", Pays: object.PaysCredits, Class: object.ClassPremium, State: "ok"}, "free"},
		{"the free plan's own usage", &object.LimitGrant{Plan: "free", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Settle: func(int64) {}}, "free"},
		{"a free model", &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassFree, State: "ok", Settle: func(int64) {}}, "free"},
		{"a caller the policy said nothing about", nil, "free"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateWith(t, 500)
			policy(t, tc.grant, nil)
			p := chatThrough("anthropic/claude-opus-5.5")
			if p.status() != http.StatusOK {
				t.Fatalf("status %d (%s)", p.status(), p.said())
			}
			if got := p.replied(controllers.LaneHeader); got != tc.lane {
				t.Fatalf("%s = %q, want %q", controllers.LaneHeader, got, tc.lane)
			}
			if free := controllers.FreeOnlyFor(p.left()); free != (tc.lane == "free") {
				t.Fatalf("the handler read the paid lane closed = %v, want %v", free, tc.lane == "free")
			}
		})
	}

	gateWith(t, 500)
	policy(t, nil, nil)
	if p := ask(http.MethodGet, "/v1/models").through(BalanceGateFilter, LaneFilter); p.replied(controllers.LaneHeader) != "" {
		t.Fatalf("listing the models was given a lane: %q", p.replied(controllers.LaneHeader))
	}
}

// WITH THE SWITCH OFF NO LANE IS NAMED. A paid plan whose included usage is used
// waits or buys credits, as it does with the switch on: 429, never another model.
func TestWithTheSwitchOffTheGateIsAsBefore(t *testing.T) {
	gateWith(t, 500)
	policy(t, &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Spend: 5_000_000_000, Settle: func(int64) {}}, nil)
	p := chatThrough("anthropic/claude-opus-5.5")
	if p.status() != http.StatusOK || p.replied(controllers.LaneHeader) != "" || p.replied(controllers.LaneReasonHeader) != "" {
		t.Fatalf("status %d lane %q reason %q: want the answer with no lane named", p.status(), p.replied(controllers.LaneHeader), p.replied(controllers.LaneReasonHeader))
	}
	for _, code := range []string{object.CodePlanAllowance, object.CodeModelCap} {
		gateWith(t, 0)
		freeModels(t, controllers.FreeModel, "enso")
		policy(t, nil, &object.LimitHit{Code: code, Class: object.ClassPremium, Fallback: "enso"})
		if p := chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions"); p.status() != http.StatusTooManyRequests || p.replied("X-Hanzo-Fallback") != "" {
			t.Fatalf("%s for a program, switch off: %d fallback %q (%s), want 429", code, p.status(), p.replied("X-Hanzo-Fallback"), p.said())
		}
	}
}

// A PAID PLAN PAST ITS INCLUDED USAGE WAITS OR BUYS CREDITS, ON EVERY SURFACE. An API
// key and a person signed in to an app alike get 429 with the plan's own code, when it
// reopens (Retry-After, X-Hanzo-Usage-Resets, resets_at) and where to buy usage
// credits (X-Hanzo-Topup-Url, topup_url) — in the headers and in the body — and are
// never handed another model, a capped model's Hanzo fallback included.
func TestAPaidPlanPastItsUsageWaitsOrBuysCreditsOnEverySurface(t *testing.T) {
	paidSwitch(t)
	gateWith(t, 0)
	freeModels(t, controllers.FreeModel, "enso")
	reset := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	for _, code := range []string{object.CodePlanAllowance, object.CodeModelCap} {
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			if q.Model == controllers.FreeModel || q.Model == "enso" {
				return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassFree, State: "limited", Settle: func(int64) {}}, nil, nil
			}
			return nil, &object.LimitHit{Code: code, Class: object.ClassPremium, Fallback: "enso", ResetsAt: reset, Upgrade: "max-20x"}, nil
		})
		for who, credential := range map[string]string{"an API key": "Bearer tok", "an app": inApp(t)} {
			p := ask(http.MethodPost, "/v1/chat/completions").with("Authorization", credential).
				body([]byte(`{"model":"anthropic/claude-opus-5.5","messages":[{"role":"user","content":"hi"}]}`)).through(BalanceGateFilter)
			if p.status() != http.StatusTooManyRequests || p.replied("X-Hanzo-Fallback") != "" || p.handed() != "" {
				t.Fatalf("%s, %s: %d fallback %q handed %q (%s), want 429 and no model", code, who, p.status(), p.replied("X-Hanzo-Fallback"), p.handed(), p.said())
			}
			pay := object.PayURL("", "acme")
			for name, want := range map[string]string{
				"X-Hanzo-Usage-Reason": code, "X-Hanzo-Usage-Resets": reset.Format(time.RFC3339), "X-Hanzo-Topup-Url": pay, "X-Hanzo-Usage": "limited",
			} {
				if got := p.replied(name); got != want {
					t.Errorf("%s, %s: %s = %q, want %q", code, who, name, got, want)
				}
			}
			if after, err := strconv.Atoi(p.replied("Retry-After")); err != nil || after < 71*3600 || after > 72*3600 {
				t.Errorf("%s, %s: Retry-After %q, want the seconds until %s", code, who, p.replied("Retry-After"), reset)
			}
			var r struct {
				Error struct {
					Message  string `json:"message"`
					Code     string `json:"code"`
					ResetsAt string `json:"resets_at"`
					TopupURL string `json:"topup_url"`
					Actions  []struct {
						Kind string `json:"kind"`
					} `json:"actions"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(p.said()), &r); err != nil {
				t.Fatal(err)
			}
			e := r.Error
			if e.Code != code || e.ResetsAt != reset.Format(time.RFC3339) || e.TopupURL != pay ||
				!strings.Contains(e.Message, "Wait until "+reset.Format(time.RFC3339)) || !strings.Contains(e.Message, "buy usage credits") || !strings.Contains(e.Message, pay) {
				t.Errorf("%s, %s: body %s", code, who, p.said())
			}
			kinds := map[string]bool{}
			for _, a := range e.Actions {
				kinds[a.Kind] = true
			}
			if !kinds["wait"] || !kinds["topup"] || !kinds["upgrade"] {
				t.Errorf("%s, %s: actions %+v, want wait, topup and upgrade", code, who, e.Actions)
			}
		}
		if p := chatWith("anthropic/claude-opus-5.5", "/v1/embeddings"); p.status() != http.StatusTooManyRequests {
			t.Fatalf("%s outside chat: %d (%s), want 429", code, p.status(), p.said())
		}
	}
}

// CREDITS A PAID PLAN BOUGHT PAY THE MODEL IT ASKED FOR. Past its included usage the
// host names the payer's cash (prepaid): the call is served as asked, on the paid
// lane, by the wallet, saying credits paid — no limit answer and no other model.
func TestBoughtCreditsPayPastThePlanWithNoInterruption(t *testing.T) {
	paidSwitch(t)
	gateWith(t, 500)
	policy(t, &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPrepaid, Class: object.ClassPremium, State: "near", Cash: true}, nil)
	p := chatThrough("anthropic/claude-opus-5.5")
	if p.status() != http.StatusOK || !strings.Contains(p.handed(), `"model":"anthropic/claude-opus-5.5"`) {
		t.Fatalf("status %d handed %s: want the asked model", p.status(), p.handed())
	}
	if p.replied("X-Hanzo-Paid-By") != object.PaysCredits || p.replied(controllers.LaneHeader) != "paid" || p.replied("X-Hanzo-Fallback") != "" {
		t.Errorf("paid-by %q lane %q fallback %q, want credits on the paid lane", p.replied("X-Hanzo-Paid-By"), p.replied(controllers.LaneHeader), p.replied("X-Hanzo-Fallback"))
	}
}

// A PLAN WITH NO ROOM FOR THE CALL IS PAST WHAT IT CAN PAY. The host is asked what pays
// past it (LimitAsk.Past): the payer's own cash serves the asked model on the paid lane
// and the plan's grant is given back; with nothing past the plan, the payer waits until
// the plan's usage reopens or buys credits — 429 with the host's code, on every
// surface, never another model.
func TestAPlanWithNoRoomIsPaidByCashOrWaits(t *testing.T) {
	paidSwitch(t)
	gateWith(t, 0)
	freeModels(t, controllers.FreeModel)
	reset := time.Now().Add(240 * time.Hour).UTC().Truncate(time.Second)
	var released, ended, pasts int
	cash := false
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		if q.Past {
			pasts++
			if cash {
				return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPrepaid, Class: object.ClassPremium, State: "near", Cash: true}, nil, nil
			}
			return nil, &object.LimitHit{Code: object.CodePlanAllowance, Class: object.ClassPremium, ResetsAt: reset, Upgrade: "max-20x"}, nil
		}
		return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Spend: 0,
			Settle: func(n int64) {
				if n == 0 {
					ended++
				}
			},
			Release: func() { released++ }}, nil, nil
	})

	for who, credential := range map[string]string{"an API key": "Bearer tok", "an app": inApp(t)} {
		released, ended, pasts = 0, 0, 0
		p := ask(http.MethodPost, "/v1/chat/completions").with("Authorization", credential).
			body([]byte(`{"model":"anthropic/claude-opus-5.5","messages":[{"role":"user","content":"hi"}]}`)).through(BalanceGateFilter, LaneFilter)
		if p.status() != http.StatusTooManyRequests || p.handed() != "" || p.replied("X-Hanzo-Fallback") != "" {
			t.Fatalf("%s, no cash: %d handed %q fallback %q (%s), want 429 and no model", who, p.status(), p.handed(), p.replied("X-Hanzo-Fallback"), p.said())
		}
		if r := refusalOf(t, p.said()); r.Error.Code != object.CodePlanAllowance || r.Error.ResetsAt != reset.Format(time.RFC3339) ||
			p.replied("Retry-After") == "" || p.replied("X-Hanzo-Topup-Url") == "" {
			t.Errorf("%s, no cash: refusal %s retry-after %q", who, p.said(), p.replied("Retry-After"))
		}
		if pasts != 1 || released != 1 || ended < 1 {
			t.Errorf("%s, no cash: asked past the plan %d time(s); the plan's grant released %d, ended %d", who, pasts, released, ended)
		}
	}

	cash = true
	gateWith(t, 500)
	released, ended, pasts = 0, 0, 0
	p := chatThrough("anthropic/claude-opus-5.5")
	if p.status() != http.StatusOK || !strings.Contains(p.handed(), `"model":"anthropic/claude-opus-5.5"`) {
		t.Fatalf("cash past the plan: %d handed %s, want the asked model", p.status(), p.handed())
	}
	if p.replied(controllers.LaneHeader) != "paid" || p.replied(controllers.LaneReasonHeader) != "" || p.replied("X-Hanzo-Paid-By") != object.PaysCredits {
		t.Errorf("lane %q reason %q paid-by %q, want the paid lane, paid by credits", p.replied(controllers.LaneHeader), p.replied(controllers.LaneReasonHeader), p.replied("X-Hanzo-Paid-By"))
	}
	if pasts != 1 || released != 1 || ended < 1 {
		t.Errorf("asked past the plan %d time(s); the plan's grant released %d, ended %d, want it given back", pasts, released, ended)
	}

	// A Hanzo SKU the plan has no room for waits too: no free rung stands in for it.
	cash = false
	gateWith(t, 0)
	if p := chatThrough("enso-pro"); p.status() != http.StatusTooManyRequests || p.handed() != "" {
		t.Fatalf("enso-pro with no room: %d handed %q (%s), want 429", p.status(), p.handed(), p.said())
	}
}

// held runs p through the gate and the lane filter to a handler that answers with a
// stream, so what the request holds on the paid lane stays held, as a stream's does
// until its writer settles it. It reports the model the handler was handed ("" when
// the gate answered), whether the request was on the paid lane, and the answer.
func held(p probe) (model string, paid bool, status int, said string) {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	app.Use(zip.H(BalanceGateFilter))
	app.Use(zip.H(LaneFilter))
	app.Raw(zip.MethodAll, "/*", func(c *zip.Ctx) error {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(c.Body(), &body)
		model, paid = body.Model, !controllers.FreeOnlyFor(c.Context())
		c.Fiber().Response().SetBodyStream(strings.NewReader("data: [DONE]\n\n"), -1)
		return nil
	})
	req := httptest.NewRequest(p.Method(), p.Path(), bytes.NewReader(p.Fiber().Request().Body()))
	p.Fiber().Request().Header.VisitAll(func(k, v []byte) { req.Header.Set(string(k), string(v)) })
	resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		panic("held: " + err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return model, paid, resp.StatusCode, string(raw)
}

// CALLS IN FLIGHT NEVER PASS THE PLAN. Fifty concurrent 600 KB prompts at a premium
// model, from a payer whose plan has $2 of its class left: each holds its quote (a
// token per byte at the model's $1 per million, about $0.62) while it streams, so
// three are seated on the paid lane and every other one is told to wait seconds or
// buy credits (429 paid_lane_full), never answered by another model. A payer with
// $0.01 left is seated for none and waits for the plan to reopen (plan_allowance_used).
func TestCallsInFlightNeverPassThePlan(t *testing.T) {
	paidSwitch(t)
	gateWith(t, 0)
	freeModels(t, controllers.FreeModel)
	for _, tc := range []struct {
		org    string
		left   int64
		seated int
		code   string
	}{{"bigco", 2_000_000_000, 3, controllers.ReasonFull}, {"lowco", 10_000_000, 0, object.CodePlanAllowance}} {
		balanceGate.setUserKeyCache("tok-"+tc.org, "", tc.org, tc.org, tc.org+"/bo")
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			if q.Past {
				return nil, &object.LimitHit{Code: object.CodePlanAllowance, Class: object.ClassPremium, ResetsAt: time.Now().Add(time.Hour)}, nil
			}
			return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Spend: tc.left, Settle: func(int64) {}}, nil, nil
		})
		prompt := strings.Repeat("x", 600_000)
		body := []byte(`{"model":"anthropic/claude-opus-5.5","max_tokens":4096,"messages":[{"role":"user","content":"` + prompt + `"}]}`)
		var mu sync.Mutex
		var wg sync.WaitGroup
		seated, waited := 0, 0
		for range 50 {
			wg.Go(func() {
				model, paid, status, said := held(ask(http.MethodPost, "/v1/chat/completions").with("Authorization", "Bearer tok-"+tc.org).body(body))
				mu.Lock()
				defer mu.Unlock()
				switch {
				case paid && model == "anthropic/claude-opus-5.5":
					seated++
				case model == "" && status == http.StatusTooManyRequests && strings.Contains(said, `"code":"`+tc.code+`"`):
					waited++
				}
			})
		}
		wg.Wait()
		if seated != tc.seated || waited != 50-tc.seated {
			t.Fatalf("%s with %d nano left: %d seated, %d told to wait (%s); want %d and %d", tc.org, tc.left, seated, waited, tc.code, tc.seated, 50-tc.seated)
		}
	}
}

// A CASH PAYER HAS NO CEILING ON CALLS IN FLIGHT. Fifty concurrent calls a payer's own
// prepaid cash pays are all seated on the paid lane at once: the wallet each handler
// holds is the only bound.
func TestACashPayerHasNoCeilingOnCallsInFlight(t *testing.T) {
	paidSwitch(t)
	gateWith(t, 0)
	balanceGate.setUserKeyCache("tok-cashco", "", "cashco", "cashco", "cashco/bo")
	balanceGate.ledger.SetBalance("cashco", 1_000_000)
	policy(t, &object.LimitGrant{Pays: object.PaysPrepaid, Class: object.ClassPremium, State: "ok", Cash: true}, nil)
	body := []byte(`{"model":"anthropic/claude-opus-5.5","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`)
	var mu sync.Mutex
	var wg sync.WaitGroup
	seated := 0
	for range 50 {
		wg.Go(func() {
			model, paid, _, _ := held(ask(http.MethodPost, "/v1/chat/completions").with("Authorization", "Bearer tok-cashco").body(body))
			mu.Lock()
			defer mu.Unlock()
			if paid && model == "anthropic/claude-opus-5.5" {
				seated++
			}
		})
	}
	wg.Wait()
	if seated != 50 {
		t.Fatalf("%d of 50 cash-paid calls seated at once, want every one", seated)
	}
}

// A payer with no plan past what they hold is not handed on: only a paid plan's
// included usage carries limited free usage past it.
func TestAProgramWithNoPlanIsStillRefused(t *testing.T) {
	gateWith(t, 0)
	policy(t, nil, &object.LimitHit{Code: object.CodePaidPlan, Class: object.ClassPremium, Message: "Claude needs a paid plan or prepaid balance. Upgrade or add prepaid credit:"})
	if p := chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions"); p.status() != http.StatusPaymentRequired {
		t.Fatalf("no plan, no fallback asked: %d (%s)", p.status(), p.said())
	}
}
