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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/ai/web"
)

func billingCtx(auth, requested string) *web.Context {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if requested != "" {
		req.Header.Set("X-Org-Id", requested)
	}
	ctx := web.NewContext()
	ctx.Reset(httptest.NewRecorder(), req)
	return ctx
}

// TestUserKeyCacheIsScopedToTheOrg pins the reason the cache key grew an org
// half. One bearer token resolves to ONE billing identity PER ORG: the same
// person acting at home pays from a personal wallet, and acting in a team org
// pays from that org's. Keyed on the token alone, whichever request arrived
// first would pin the answer for five minutes and the gate would read the wrong
// wallet — checking one balance while the controller drains another.
func TestUserKeyCacheIsScopedToTheOrg(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)

	bg.setUserKeyCache("tok", "", "hanzo/alice", "hanzo", "hanzo/alice")
	bg.setUserKeyCache("tok", "acme", "acme", "acme", "hanzo/alice")

	s, ns, _, ok := bg.getUserKeyCached("tok", "")
	if !ok || s != "hanzo/alice" || ns != "hanzo" {
		t.Fatalf("home entry = (%q, %q, %v), want (hanzo/alice, hanzo, true)", s, ns, ok)
	}
	s, ns, _, ok = bg.getUserKeyCached("tok", "acme")
	if !ok || s != "acme" || ns != "acme" {
		t.Fatalf("switched entry = (%q, %q, %v), want (acme, acme, true)", s, ns, ok)
	}
	// An org nobody cached is a MISS, never another org's answer.
	if _, _, _, ok := bg.getUserKeyCached("tok", "zoo"); ok {
		t.Fatal("an uncached org hit the cache — one org's wallet served another's request")
	}
}

// TestResolveBillingKeyIgnoresAnUnbackedOrgHeader is the NO-OP proof for the
// router gate: an sk- IAM key carries no signed `orgs` claim, so the X-Org-Id a
// client stamps on it moves nothing. The cached home answer is returned for the
// home request, and the switch attempt does NOT reuse it — it misses, resolves
// nothing (no IAM configured in test), and bills no one.
func TestResolveBillingKeyIgnoresAnUnbackedOrgHeader(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })

	// The home answer, as the gate would have cached it on a first request.
	bg.setUserKeyCache("sk-abc", "", "acme", "acme", "acme/user")

	subject, namespace, _ := resolveBillingKey(billingCtx("Bearer sk-abc", ""))
	if subject != "acme" || namespace != "acme" {
		t.Fatalf("home request = (%q, %q), want (acme, acme)", subject, namespace)
	}

	// Same token, a client-asserted org. It must NOT be served the home entry as
	// if it were the switched org's, and it must not be served the switched org's
	// wallet either — an sk- key has no membership proof, so it can only ever
	// resolve its own owner.
	subject, namespace, _ = resolveBillingKey(billingCtx("Bearer sk-abc", "zoo"))
	if namespace == "zoo" {
		t.Fatalf("an unbacked X-Org-Id moved the ledger to %q", namespace)
	}
	if subject == "acme" && namespace == "acme" {
		t.Fatal("the switch attempt was served the cached HOME entry — the cache is not org-scoped")
	}
}
