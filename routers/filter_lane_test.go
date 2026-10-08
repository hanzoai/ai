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
	stdcontext "context"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
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
		{"a subscriber whose plan pays", &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok", Settle: func(int64) {}}, "paid"},
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
		return &object.LimitGrant{Plan: "max-20x", Pays: object.PaysPlan, Class: object.ClassPremium, State: "ok",
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

// A payer with no plan past what they hold is not handed on: only a paid plan's
// included usage carries limited free usage past it.
func TestAProgramWithNoPlanIsStillRefused(t *testing.T) {
	gateWith(t, 0)
	policy(t, nil, &object.LimitHit{Code: object.CodePaidPlan, Class: object.ClassPremium, Message: "Claude needs a paid plan or prepaid balance. Upgrade or add prepaid credit:"})
	if p := chatWith("anthropic/claude-opus-5.5", "/v1/chat/completions"); p.status() != http.StatusPaymentRequired {
		t.Fatalf("no plan, no fallback asked: %d (%s)", p.status(), p.said())
	}
}
