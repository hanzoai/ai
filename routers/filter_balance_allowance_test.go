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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// A ZERO-PRICED ROUTE IS BOUNDED BY THE PLAN ALLOWANCE, AND BY NOTHING ELSE.
//
// The wallet has nothing to refuse at zero, so without this the free pool is
// unlimited for anyone who can name a free model. The allowance is fifty calls per
// person per UTC day, and the host counts each call as it admits it: asking is
// taking. Call fifty-one is refused 429 allowance_spent — a code distinct from
// insufficient_balance, because the caller is not broke, they are done for the day,
// and the cure is waiting for 00:00 UTC or adding credits rather than a top-up.
//
// It fails OPEN for a caller the host can name, and the direction is safe only here:
// the route costs nothing, so an unanswerable allowance hands out our own compute
// and never a paid vendor call.
func TestAllowanceBoundsTheFreeRoute(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user") // resolveBillingKey → subject "acme"
	bg.ledger.SetBalance("acme", 0)                            // no wallet at all — the free caller

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetSpent(nil) }) // never leak the hook into other tests

	free := `{"model":"enso-free","messages":[]}`
	call := func() probe {
		// The identity boundary in front stamps the person; the probe stands in for it.
		return ask(http.MethodPost, "/v1/chat/completions").
			with("Authorization", "Bearer tok").
			with(zip.HeaderUserOwner, "acme").
			with(zip.HeaderUser, "u-ann").
			body([]byte(free)).
			through(BalanceGateFilter)
	}

	// 1. No hook installed (standalone ai): the free route is served, unchanged, and
	//    says nothing about a ceiling it does not have.
	if p := call(); p.status() != http.StatusOK || p.replied("X-RateLimit-Limit") != "" {
		t.Errorf("no allowance installed: status %d, X-RateLimit-Limit %q — the default must be unchanged behavior",
			p.status(), p.replied("X-RateLimit-Limit"))
	}

	// 2. Admitted: served, and the host was told who pays AND who is calling. The
	//    person is read off the context with zip.CallerOf, so the hook must be handed
	//    one bound to the request in flight — a raw handler's own context names nobody,
	//    and the host would count every member of acme as one.
	resets := time.Now().Add(5 * time.Hour).UTC().Truncate(time.Second)
	var saw struct {
		subject, namespace string
		who                zip.Caller
		restated           string
	}
	object.SetSpent(func(ctx stdcontext.Context, subject, namespace string) (object.Standing, error) {
		saw.subject, saw.namespace, saw.who = subject, namespace, zip.CallerOf(ctx)
		// The host restates the org its plane call is for; with a request behind the
		// context that statement would be ignored and the request's headers forwarded.
		saw.restated = zip.CallerOf(zip.WithCaller(ctx, zip.Caller{Org: "billing"})).Org
		return object.Standing{Window: "day", Limit: 50, Used: 12, Resets: resets}, nil
	})

	// THE GATE RECORDS NO USAGE. The hook is the count and the usage recorder is the
	// money; a free call spends its unit in the one and nothing in the other.
	t.Cleanup(func() { object.SetUsageRecorder(nil) })
	object.SetUsageRecorder(func(_ stdcontext.Context, u object.UsageEvent) error {
		t.Errorf("the balance gate recorded usage %+v; only a served call may be recorded", u)
		return nil
	})

	p := call()
	if p.status() != http.StatusOK {
		t.Fatalf("a caller with calls left was refused: %d (%s)", p.status(), p.said())
	}
	if saw.subject != "acme" || saw.namespace != "acme" {
		t.Errorf("allowance got subject=%q namespace=%q, want acme/acme", saw.subject, saw.namespace)
	}
	if saw.restated != "billing" {
		t.Errorf("the hook's context carries the request: a restated org reads %q, so the host's plane call would forward a request that may name no org", saw.restated)
	}
	if saw.who.Owner != "acme" || saw.who.User != "u-ann" {
		t.Errorf("the hook's context names owner=%q user=%q, want acme/u-ann — the host cannot count a person it cannot see",
			saw.who.Owner, saw.who.User)
	}
	for h, want := range map[string]string{
		"X-RateLimit-Limit":     "50",
		"X-RateLimit-Remaining": "38",
		"X-RateLimit-Reset":     fmt.Sprint(resets.Unix()),
	} {
		if got := p.replied(h); got != want {
			t.Errorf("admitted: %s = %q, want %q", h, got, want)
		}
	}

	// 3. Spent: 429 with the distinct code and the rule in one sentence, the pay page
	//    beside it, and nothing left in the headers.
	midnight := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	object.SetSpent(func(_ stdcontext.Context, _, _ string) (object.Standing, error) {
		return object.Standing{Spent: true, Window: "day", Limit: 50, Used: 50, Resets: midnight}, nil
	})
	p = call()
	code, body := p.status(), p.said()
	if code != http.StatusTooManyRequests {
		t.Fatalf("a spent allowance returned %d, want 429 (%s)", code, body)
	}
	var got struct {
		Error struct {
			Message    string `json:"message"`
			Type       string `json:"type"`
			Code       string `json:"code"`
			ResetsAt   string `json:"resets_at"`
			UpgradeURL string `json:"upgrade_url"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("refusal body %q: %v", body, err)
	}
	if want := "Free tier: 50 calls per day. Resets at 00:00 UTC. Add credits to use paid models."; got.Error.Message != want {
		t.Errorf("message %q, want %q", got.Error.Message, want)
	}
	if got.Error.Code != "allowance_spent" || got.Error.Type != "insufficient_quota" {
		t.Errorf("refusal code=%q type=%q, want allowance_spent/insufficient_quota", got.Error.Code, got.Error.Type)
	}
	if got.Error.ResetsAt != midnight.Format(time.RFC3339) {
		t.Errorf("resets_at = %q, want %q", got.Error.ResetsAt, midnight.Format(time.RFC3339))
	}
	if got.Error.UpgradeURL != object.PayURL("", "acme") || got.Error.UpgradeURL == "" {
		t.Errorf("upgrade_url = %q, want the pay page %q", got.Error.UpgradeURL, object.PayURL("", "acme"))
	}
	if ra := p.replied("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q, want the seconds until 00:00 UTC", ra)
	}
	for h, want := range map[string]string{
		"X-RateLimit-Limit":     "50",
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     fmt.Sprint(midnight.Unix()),
	} {
		if got := p.replied(h); got != want {
			t.Errorf("refused: %s = %q, want %q", h, got, want)
		}
	}
	if strings.Contains(body, "insufficient_balance") {
		t.Error("a spent allowance was reported as an empty wallet — the two have different cures")
	}

	// 4. Retrieval is not counted: an embedding or a rerank on the same free route
	//    never asks the allowance, spent or not.
	for _, path := range []string{"/v1/embeddings", "/v1/rerank"} {
		p := ask(http.MethodPost, path).
			with("Authorization", "Bearer tok").
			with(zip.HeaderUserOwner, "acme").
			with(zip.HeaderUser, "u-ann").
			body([]byte(`{"model":"enso-free","input":"x"}`)).
			through(BalanceGateFilter)
		if p.status() != http.StatusOK || p.replied("X-RateLimit-Limit") != "" {
			t.Errorf("%s with the day spent: status %d, X-RateLimit-Limit %q — retrieval must not spend or be refused by the allowance",
				path, p.status(), p.replied("X-RateLimit-Limit"))
		}
	}

	// 5. Reader error: fails OPEN, and states no standing it does not have.
	object.SetSpent(func(_ stdcontext.Context, _, _ string) (object.Standing, error) {
		return object.Standing{Spent: true, Limit: 50}, errors.New("store unreachable")
	})
	if p := call(); p.status() != http.StatusOK || p.replied("X-RateLimit-Limit") != "" {
		t.Errorf("an allowance read error: status %d, X-RateLimit-Limit %q — it must fail open and say nothing",
			p.status(), p.replied("X-RateLimit-Limit"))
	}
}

// THE ALLOWANCE NEVER TOUCHES A PRICED ROUTE. Money bounds those, and a counter that
// also refused them would be a second paywall with its own opinion — including one
// that could ADMIT a call the wallet refuses. The gate order proves it: a priced
// model at $0 is 402 insufficient_balance whatever the allowance says.
func TestAllowanceLeavesThePaywallAlone(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user")
	bg.ledger.SetBalance("acme", 0)

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetSpent(nil) })

	called := false
	object.SetSpent(func(_ stdcontext.Context, _, _ string) (object.Standing, error) {
		called = true
		return object.Standing{}, nil // "allowance left" must not rescue a caller with no money
	})

	p := ask(http.MethodPost, "/v1/chat/completions").
		with("Authorization", "Bearer tok").
		body([]byte(`{"model":"gpt-4o","messages":[]}`))
	p = p.through(BalanceGateFilter)

	if p.status() != http.StatusPaymentRequired {
		t.Fatalf("a priced model at $0 returned %d, want 402 — the paywall must hold", p.status())
	}
	if !strings.Contains(p.said(), "insufficient_balance") {
		t.Errorf("refusal body %q is not the wallet's — a priced route must refuse as unpaid", p.said())
	}
	if called {
		t.Error("the allowance was read for a PRICED call — it bounds the free lane only")
	}
}
