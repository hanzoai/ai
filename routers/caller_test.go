// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package routers

import (
	stdcontext "context"
	"net/http"
	"testing"

	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// stated is what ai.For does to a request: a program in this process states the
// customer the call is for.
func stated(who object.Caller) zip.Handler {
	return func(c *zip.Ctx) error {
		object.SetCaller(c.Fiber().RequestCtx(), who)
		return c.Continue()
	}
}

// A call a program in this process made for a customer is covered by that
// program's meter: an empty deployment wallet does not refuse it, the plan is not
// asked, and the answer says the caller pays.
func TestACallersCallAsksNoWallet(t *testing.T) {
	gateWith(t, 0)
	asked := 0
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked++
		return nil, &object.LimitHit{Code: object.CodeUsageCap}, nil
	})
	who := object.Caller{Org: "globex", Person: "globex/ada", Project: "search"}
	p := ask(http.MethodPost, "/v1/embeddings").with("Authorization", "Bearer tok").
		body([]byte(`{"model":"zen-embedding","input":"hi"}`)).
		through(stated(who), CallerFilter, BalanceGateFilter)
	if p.status() != http.StatusOK || p.replied("X-Hanzo-Paid-By") != object.PaysCaller || asked != 0 {
		t.Fatalf("a caller's call with an empty wallet: %d paid-by %q, plan asked %d (%s)", p.status(), p.replied("X-Hanzo-Paid-By"), asked, p.said())
	}
	if got, ok := object.CallerOf(p.left()); !ok || got != who {
		t.Fatalf("the handler read caller %+v (%v), want %+v", got, ok, who)
	}
}

// The same request from the network states no caller, whatever it carries, and
// meets the policy that decides who pays.
func TestARequestFromTheNetworkStatesNoCaller(t *testing.T) {
	gateWith(t, 0)
	asked := 0
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked++
		return nil, &object.LimitHit{Code: object.CodeUsageCap}, nil
	})
	p := ask(http.MethodPost, "/v1/embeddings").with("Authorization", "Bearer tok").
		with("X-User-Id", "globex/ada").with("X-Project-Id", "search").
		body([]byte(`{"model":"zen-embedding","input":"hi"}`)).
		through(CallerFilter, BalanceGateFilter)
	if p.status() != http.StatusTooManyRequests || asked != 1 {
		t.Fatalf("a network request answered %d with the plan asked %d times, want the plan's 429 (%s)", p.status(), asked, p.said())
	}
	if _, ok := object.CallerOf(p.left()); ok {
		t.Fatal("a network request stated a caller")
	}
}

// A caller's call is attributed to the customer it names: the org, the person and
// the project, never the headers the credential's own request would carry.
func TestACallersCallIsTheCustomers(t *testing.T) {
	who := object.Caller{Org: "globex", Person: "globex/ada", Project: "search"}
	p := ask(http.MethodPost, "/v1/embeddings").with("X-Project-Id", "other").
		through(stated(who), CallerFilter, TenantContextFilter)
	attr := object.GenAIAttributionFromContext(p.left())
	if attr.Org != "globex" || attr.User != "globex/ada" || attr.Project != "search" {
		t.Fatalf("attributed to %+v, want globex, globex/ada, search", attr)
	}
}
