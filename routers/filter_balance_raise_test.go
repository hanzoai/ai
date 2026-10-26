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
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// HOW A REFUSED CALLER RAISES ITS LIMIT IS THE HOST'S TO SAY (object.RaiseHook), and
// every gate refusal a limit lifts carries it: the tier and the one API call beside the
// code, and on a 429 the ceiling as X-RateLimit-*. A host that says nothing changes
// nothing: the goldens below are the bytes each refusal answered with before the hook
// existed (c7f85eedd), captured through these same requests.

var raiseReset = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

type raiseCase struct {
	code   string
	status int
	model  string
	setup  func(t *testing.T)
	golden string
	// limit is X-RateLimit-Limit with no hook, "" where the refusal sets none.
	limit string
}

func raiseCases() []raiseCase {
	return []raiseCase{
		{
			code: object.CodeAllowanceSpent, status: http.StatusTooManyRequests, model: "enso-free", limit: "50",
			setup: func(t *testing.T) {
				freeModels(t, "enso-free")
				object.SetSpent(func(stdcontext.Context, string, string) (object.Standing, error) {
					return object.Standing{Spent: true, Window: "day", Limit: 50, Used: 50, Resets: raiseReset}, nil
				})
			},
			golden: `{"error":{"message":"Free tier: 50 calls per day. Resets at 00:00 UTC. Add credits to use paid models.","type":"insufficient_quota","code":"allowance_spent","resets_at":"2099-01-01T00:00:00Z","upgrade_url":"https://hanzo.ai/pay"}}`,
		},
		{
			code: object.CodeFreePlanCap, status: http.StatusTooManyRequests, model: "anthropic/claude-opus-5.5",
			setup: func(t *testing.T) {
				policy(t, nil, &object.LimitHit{Code: object.CodeFreePlanCap, Class: object.ClassPremium, Model: "anthropic/claude-opus-5.5", ResetsAt: raiseReset, Upgrade: "pro"})
			},
			golden: `{"error":{"message":"This request is outside what your plan includes. Wait until 2099-01-01T00:00:00Z, or buy usage credits to go on now: https://hanzo.ai/pay","type":"rate_limit_error","code":"free_plan_cap","class":"premium","model":"anthropic/claude-opus-5.5","resets_at":"2099-01-01T00:00:00Z","topup_url":"https://hanzo.ai/pay","upgrade_url":"https://hanzo.ai/pay/cart?plan=pro","actions":[{"kind":"wait","label":"Wait until 2099-01-01T00:00:00Z"},{"kind":"topup","label":"Buy usage credits","url":"https://hanzo.ai/pay"},{"kind":"upgrade","label":"Upgrade your plan","url":"https://hanzo.ai/pay/cart?plan=pro","plan":"pro"}]}}`,
		},
		{
			code: object.CodePaidPlan, status: http.StatusPaymentRequired, model: "anthropic/claude-opus-5.5",
			setup: func(t *testing.T) {
				policy(t, nil, &object.LimitHit{Code: object.CodePaidPlan, Class: object.ClassPremium, Message: "Claude needs a paid plan or prepaid balance. Upgrade or add prepaid credit:"})
			},
			golden: `{"error":{"message":"Claude needs a paid plan or prepaid balance. Upgrade or add prepaid credit. Add funds or upgrade: https://hanzo.ai/pay","type":"billing_error","code":"paid_plan_required","class":"premium","topup_url":"https://hanzo.ai/pay","actions":[{"kind":"topup","label":"Buy usage credits","url":"https://hanzo.ai/pay"}]}}`,
		},
		{
			code: object.CodeInsufficientBalance, status: http.StatusPaymentRequired, model: "anthropic/claude-opus-5.5",
			setup:  func(*testing.T) {},
			golden: `{"error":{"message":"Insufficient balance. Add credits to your wallet at https://hanzo.ai/pay","type":"billing_error","code":"insufficient_balance"}}`,
		},
	}
}

// refusedAs runs one case's request with hook f installed: acme/ann on an API key,
// paying from an empty acme wallet, the person stamped the way the identity boundary in
// front stamps it.
func refusedAs(t *testing.T, rc raiseCase, f object.RaiseFunc) probe {
	t.Helper()
	gateWith(t, 0)
	rc.setup(t)
	object.SetRaise(f)
	t.Cleanup(func() { object.SetRaise(nil) })
	return ask(http.MethodPost, "/v1/chat/completions").
		with("Authorization", "Bearer tok").
		with(zip.HeaderUserOwner, "acme").
		with(zip.HeaderUser, "u-ann").
		body([]byte(`{"model":"` + rc.model + `","max_tokens":1000,"messages":[{"role":"user","content":"hi"}]}`)).
		through(BalanceGateFilter)
}

// asked is what the host's hook was handed.
type asked struct {
	n                        int
	subject, namespace, code string
	owner, user              string
	deadline                 time.Duration
}

// answering is a hook that records what it was handed and answers r.
func answering(saw *asked, r *object.Raise) object.RaiseFunc {
	return func(ctx stdcontext.Context, subject, namespace, code string) *object.Raise {
		who := zip.CallerOf(ctx)
		d, _ := ctx.Deadline()
		*saw = asked{n: saw.n + 1, subject: subject, namespace: namespace, code: code,
			owner: who.Owner, user: who.User, deadline: time.Until(d)}
		return r
	}
}

func unchanged(t *testing.T, rc raiseCase, p probe) {
	t.Helper()
	if p.status() != rc.status || p.said() != rc.golden {
		t.Errorf("got  %d %s\nwant %d %s", p.status(), p.said(), rc.status, rc.golden)
	}
	if got := p.replied("X-RateLimit-Limit"); got != rc.limit {
		t.Errorf("X-RateLimit-Limit %q, want %q", got, rc.limit)
	}
	if ct := p.replied("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q, want application/json", ct)
	}
}

// No hook, or a hook that names nothing for the caller: every refusal is the bytes and
// headers it was. The hook is still asked, with the wallet, the person, the refusal's
// own code and a deadline.
func TestRaiseNilLeavesEveryRefusalAsItWas(t *testing.T) {
	for _, rc := range raiseCases() {
		t.Run(rc.code+"/no hook", func(t *testing.T) {
			unchanged(t, rc, refusedAs(t, rc, nil))
		})
		t.Run(rc.code+"/hook answers nil", func(t *testing.T) {
			var saw asked
			unchanged(t, rc, refusedAs(t, rc, answering(&saw, nil)))
			want := asked{n: 1, subject: "acme", namespace: "acme", code: rc.code, owner: "acme", user: "u-ann"}
			got := saw
			got.deadline = 0
			if got != want {
				t.Errorf("hook handed %+v, want %+v", got, want)
			}
			if saw.deadline <= 0 || saw.deadline > raiseWait {
				t.Errorf("hook deadline %v away, want within %v", saw.deadline, raiseWait)
			}
		})
	}
}

// A hook that names the call: tier and raise are added to the error object after every
// field the site wrote, and nothing else in the body moves. A 429 says the ceiling on
// X-RateLimit-*; where the allowance already said where the caller stands, that stands.
// A 402 is no rate limit and says none.
func TestRaiseNamesTheTierAndTheCall(t *testing.T) {
	r := &object.Raise{
		Tier: "agent", Limit: 5, Window: "day", Message: "the host's sentence",
		Method: http.MethodPost, Path: "/v1/example/raise", Body: map[string]any{"amount": 5},
	}
	tail := `,"tier":"agent","raise":{"method":"POST","path":"/v1/example/raise","body":{"amount":5},"message":"the host's sentence"}}}`
	reset := fmt.Sprint(raiseReset.Unix())
	for _, rc := range raiseCases() {
		t.Run(rc.code, func(t *testing.T) {
			var saw asked
			p := refusedAs(t, rc, answering(&saw, r))
			want := rc.golden[:len(rc.golden)-2] + tail
			if p.status() != rc.status || p.said() != want {
				t.Errorf("got  %d %s\nwant %d %s", p.status(), p.said(), rc.status, want)
			}
			if saw.n != 1 || saw.code != rc.code {
				t.Errorf("hook asked %d times with code %q, want once with %q", saw.n, saw.code, rc.code)
			}
			heads := map[string]string{"X-RateLimit-Limit": "", "X-RateLimit-Remaining": "", "X-RateLimit-Reset": ""}
			switch rc.code {
			case object.CodeAllowanceSpent: // standing said it first: 50, not the host's 5
				heads = map[string]string{"X-RateLimit-Limit": "50", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}
			case object.CodeFreePlanCap:
				heads = map[string]string{"X-RateLimit-Limit": "5", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}
			}
			for h, v := range heads {
				if got := p.replied(h); got != v {
					t.Errorf("%s = %q, want %q", h, got, v)
				}
			}
		})
	}
}

// A host that names the call and no ceiling (Limit 0) says no X-RateLimit-* on a 429:
// a header naming a ceiling of nothing is a lie, not an omission.
func TestRaiseWithNoLimitSetsNoRateLimitHeaders(t *testing.T) {
	rc := raiseCases()[1] // free_plan_cap, a 429 that sets no headers of its own
	p := refusedAs(t, rc, func(stdcontext.Context, string, string, string) *object.Raise {
		return &object.Raise{Tier: "agent", Message: "m", Method: http.MethodPost, Path: "/v1/example/raise"}
	})
	want := rc.golden[:len(rc.golden)-2] + `,"tier":"agent","raise":{"method":"POST","path":"/v1/example/raise","message":"m"}}}`
	if p.status() != http.StatusTooManyRequests || p.said() != want {
		t.Errorf("got  %d %s\nwant 429 %s", p.status(), p.said(), want)
	}
	for _, h := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
		if got := p.replied(h); got != "" {
			t.Errorf("%s = %q with no limit named, want none", h, got)
		}
	}
}

// A hook that never answers, or panics, never breaks a refusal: it goes out unchanged,
// and a hung host costs the caller raiseWait, not its own time.
func TestRaiseThatHangsOrPanicsLeavesTheRefusal(t *testing.T) {
	for _, rc := range raiseCases() {
		t.Run(rc.code+"/hangs", func(t *testing.T) {
			hold := make(chan struct{})
			t.Cleanup(func() { close(hold) })
			began := time.Now()
			p := refusedAs(t, rc, func(stdcontext.Context, string, string, string) *object.Raise {
				<-hold
				return &object.Raise{Tier: "agent", Method: http.MethodPost, Path: "/v1/example/raise"}
			})
			unchanged(t, rc, p)
			if took := time.Since(began); took < raiseWait || took > raiseWait+2*time.Second {
				t.Errorf("a hung hook held the refusal %v, want about %v", took, raiseWait)
			}
		})
		t.Run(rc.code+"/panics", func(t *testing.T) {
			unchanged(t, rc, refusedAs(t, rc, func(stdcontext.Context, string, string, string) *object.Raise {
				panic("host bug")
			}))
		})
	}
}

// The hook is asked only of a refusal a limit lifts: never of an admitted call, and
// never of the 503 a gate gives when it cannot read — paying does not lift an outage.
func TestRaiseIsNotAskedWhenAdmittedOrUnreadable(t *testing.T) {
	admitted := raiseCase{model: "enso-free", setup: func(t *testing.T) {
		freeModels(t, "enso-free")
		object.SetSpent(func(stdcontext.Context, string, string) (object.Standing, error) {
			return object.Standing{Window: "day", Limit: 50, Used: 3, Resets: raiseReset}, nil
		})
	}}
	unreadable := raiseCase{model: "anthropic/claude-opus-5.5", setup: func(*testing.T) {
		object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			return nil, nil, errors.New("policy store down")
		})
	}}
	for _, c := range []struct {
		name   string
		rc     raiseCase
		status int
	}{{"admitted", admitted, http.StatusOK}, {"unreadable", unreadable, http.StatusServiceUnavailable}} {
		t.Run(c.name, func(t *testing.T) {
			var saw asked
			p := refusedAs(t, c.rc, answering(&saw, &object.Raise{Tier: "agent", Limit: 5}))
			if p.status() != c.status || saw.n != 0 {
				t.Errorf("status %d, hook asked %d times (%s), want %d and never asked", p.status(), saw.n, p.said(), c.status)
			}
		})
	}
}
