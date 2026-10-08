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
	"net/http"
	"net/http/httptest"
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

// THE LANE FOLLOWS THE GATE'S GRANT. A subscriber whose plan pays is on the paid lane
// and told so; a payer with no plan, and a caller the policy said nothing about, are
// on the free lane. A path no policy governs carries no lane.
func TestTheLaneFollowsTheGatesGrant(t *testing.T) {
	paidSwitch(t)
	for _, tc := range []struct {
		name  string
		grant *object.LimitGrant
		lane  string
	}{
		{"a subscriber whose plan pays", &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Spend: 5_000_000_000, Settle: func(int64) {}}, "paid"},
		{"a subscriber whose prepaid pays past the plan", &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPrepaid, Class: object.ClassPremium, State: "near"}, "paid"},
		{"a wallet with no plan", &object.LimitGrant{Pays: object.PaysPrepaid, Class: object.ClassPremium, State: "ok"}, "free"},
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

// WITH THE SWITCH OFF NOTHING CHANGES. No lane is named on any answer, and a program
// whose plan's included usage is used still gets its 402 unless it asked for fallback.
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
		if p := chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions"); p.status() != http.StatusPaymentRequired {
			t.Fatalf("%s for a program, switch off: %d (%s), want 402", code, p.status(), p.said())
		}
	}
}

// WITH THE SWITCH ON, A PAID PLAN PAST ITS INCLUDED USAGE IS ANSWERED ON ANY CLIENT.
// Limited free usage past the included usage is part of every paid plan, so a program
// is answered by the free model, or a capped model's fallback, saying so.
func TestWithTheSwitchOnAPaidPlanPastItsUsageIsAnsweredOnAnyClient(t *testing.T) {
	paidSwitch(t)
	gateWith(t, 0)
	freeModels(t, controllers.FreeModel, "enso")
	for code, to := range map[string]string{object.CodePlanAllowance: controllers.FreeModel, object.CodeModelCap: "enso"} {
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			if q.Model == to {
				return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassFree, State: "limited", Settle: func(int64) {}}, nil, nil
			}
			return nil, &object.LimitHit{Code: code, Class: object.ClassPremium, Fallback: "enso"}, nil
		})
		p := chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions")
		if p.status() != http.StatusOK || p.replied("X-Hanzo-Fallback") != to || !strings.Contains(p.handed(), `"model":"`+to+`"`) {
			t.Fatalf("%s for a program, switch on: %d fallback %q (%s), want %s", code, p.status(), p.replied("X-Hanzo-Fallback"), p.said(), to)
		}
		if p := chatWith("anthropic/claude-opus-5.5", "/v1/embeddings"); p.status() != http.StatusPaymentRequired {
			t.Fatalf("%s outside chat: %d (%s), want 402", code, p.status(), p.said())
		}
	}
}

// PAST THE PLATFORM'S DAY A SUBSCRIBER IS ANSWERED, NOT REFUSED. A chat for a model
// only the paid lane serves is handed to the free model in limited mode, saying why,
// and what admitting the first model took is given back; a Hanzo SKU keeps its model
// and is served on the free lane by what stands in for it.
func TestPastThePlatformsDayASubscriberIsAnsweredInLimitedMode(t *testing.T) {
	paidSwitch(t)
	t.Setenv("PAID_LANE_DAILY", "0")
	gateWith(t, 0)
	freeModels(t, controllers.FreeModel)
	released, ended := 0, 0
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		if q.Model == controllers.FreeModel {
			return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassFree, State: "limited", Settle: func(int64) {}}, nil, nil
		}
		return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Spend: 5_000_000_000,
			Settle: func(n int64) {
				if n == 0 {
					ended++
				}
			},
			Release: func() { released++ }}, nil, nil
	})

	p := chatThrough("anthropic/claude-opus-5.5")
	if p.status() != http.StatusOK || !strings.Contains(p.handed(), `"model":"`+controllers.FreeModel+`"`) {
		t.Fatalf("status %d handed %s: want the free model to answer", p.status(), p.handed())
	}
	for name, want := range map[string]string{
		"X-Hanzo-Fallback":           controllers.FreeModel,
		"X-Hanzo-Usage-Reason":       controllers.ReasonCeiling,
		"X-Hanzo-Usage":              "limited",
		controllers.LaneHeader:       "free",
		controllers.LaneReasonHeader: controllers.ReasonCeiling,
	} {
		if got := p.replied(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if released != 1 || ended < 1 {
		t.Errorf("the first grant was released %d time(s) and ended %d, want it given back", released, ended)
	}

	p = chatThrough("enso-pro")
	if p.status() != http.StatusOK || !strings.Contains(p.handed(), `"model":"enso-pro"`) {
		t.Fatalf("status %d handed %s: want enso-pro kept", p.status(), p.handed())
	}
	if p.replied(controllers.LaneHeader) != "free" || p.replied(controllers.LaneReasonHeader) != controllers.ReasonCeiling {
		t.Errorf("lane %q reason %q, want free and %s", p.replied(controllers.LaneHeader), p.replied(controllers.LaneReasonHeader), controllers.ReasonCeiling)
	}
}

// held runs p through the gate and the lane filter to a handler that answers with a
// stream, so what the request holds on the paid lane stays held, as a stream's does
// until its writer settles it. It reports the model the handler was handed and
// whether the request was on the paid lane.
func held(p probe) (model string, paid bool) {
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
	if _, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second}); err != nil {
		panic("held: " + err.Error())
	}
	return model, paid
}

// CALLS IN FLIGHT NEVER PASS THE PLAN. Fifty concurrent 200,000-token prompts at a
// premium model, from a payer whose plan has $1 of its class left: each holds its
// estimate (about $0.22) while it streams, so four are seated on the paid lane and
// every other one is answered by the free model, saying why. A payer with $0.01 left
// is seated for none.
func TestCallsInFlightNeverPassThePlan(t *testing.T) {
	paidSwitch(t)
	gateWith(t, 0)
	freeModels(t, controllers.FreeModel)
	for _, tc := range []struct {
		org    string
		left   int64
		seated int
	}{{"bigco", 1_000_000_000, 4}, {"lowco", 10_000_000, 0}} {
		balanceGate.setUserKeyCache("tok-"+tc.org, "", tc.org, tc.org, tc.org+"/bo")
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			if q.Model == controllers.FreeModel {
				return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassFree, State: "limited", Settle: func(int64) {}}, nil, nil
			}
			return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Spend: tc.left, Settle: func(int64) {}}, nil, nil
		})
		prompt := strings.Repeat("x", 600_000)
		body := []byte(`{"model":"anthropic/claude-opus-5.5","max_tokens":4096,"messages":[{"role":"user","content":"` + prompt + `"}]}`)
		var mu sync.Mutex
		var wg sync.WaitGroup
		seated, free := 0, 0
		for range 50 {
			wg.Go(func() {
				model, paid := held(ask(http.MethodPost, "/v1/chat/completions").with("Authorization", "Bearer tok-"+tc.org).body(body))
				mu.Lock()
				defer mu.Unlock()
				switch {
				case paid && model == "anthropic/claude-opus-5.5":
					seated++
				case !paid && model == controllers.FreeModel:
					free++
				}
			})
		}
		wg.Wait()
		if seated != tc.seated || free != 50-tc.seated {
			t.Fatalf("%s with %d nano left: %d seated, %d answered by the free model; want %d and %d", tc.org, tc.left, seated, free, tc.seated, 50-tc.seated)
		}
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
