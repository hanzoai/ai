// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License").

package routers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	web "github.com/hanzoai/ai/web"
)

// TestResolveBillingKey_NilGateNoPanic locks in the fix for the prod-down bug:
// when balance enforcement is disabled (commerceEndpoint unconfigured =>
// InitBalanceGate returns early => balanceGate == nil), resolveBillingKey must
// resolve no billing subject rather than dereferencing the nil singleton.
//
// RateLimitFilter calls resolveBillingKey on EVERY authenticated /v1 request,
// BEFORE BalanceGateFilter's own nil guard runs, so an unguarded deref here
// panicked and returned a bare HTTP 500 on every authed request (anon requests
// carry no token and return before the deref). This must never happen again.
func TestResolveBillingKey_NilGateNoPanic(t *testing.T) {
	saved := balanceGate
	balanceGate = nil
	defer func() { balanceGate = saved }()

	// sk- IAM key: the exact token class that reached the nil deref in prod.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-deadbeefcafe")
	ctx := web.NewContext()
	ctx.Reset(rec, req)

	// Must not panic; must resolve no subject (fail-open when billing disabled).
	subject, namespace, userKey := resolveBillingKey(ctx)
	if subject != "" || namespace != "" || userKey != "" {
		t.Fatalf("nil balanceGate must resolve no billing subject, got (%q, %q, %q)", subject, namespace, userKey)
	}
}
