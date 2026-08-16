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
	"net/http/httptest"
	"strings"
	"testing"

	web "github.com/hanzoai/ai/web"

	"github.com/hanzoai/ai/object"
)

// THE PUBLIC LANE IS BOUNDED ONCE, AND THE LANE IS WHAT BOUNDS IT.
//
// This gate resolves a billing subject from an AMBIENT SESSION as readily as from a
// bearer (resolveBillingKey source 1), and the visitor widget runs on our own pages —
// so a signed-in reader asking an anonymous question arrives here as themselves. The
// gate would then hold them to THEIR plan for a call they did not make as themselves,
// while the lane holds the visitor to the visitor's — one request answering to two
// ceilings, one of which belongs to the wrong subject.
//
// The gate also reads the MODEL from the body, which is the one thing the public lane
// promises to ignore — a caller naming a priced model there steers this gate into a
// balance check on a route that never bills them.
//
// So the lane is exempt: it is free by construction, it bills nobody, and it carries
// its own ceiling. Exempt from BALANCE, never from bounds.
func TestPublicLaneIsCountedOnceAndOnlyByTheLane(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/user")
	bg.ledger.SetBalance("acme", 0)

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetSpent(nil) })

	asks := 0
	object.SetSpent(func(_ stdcontext.Context, subject, namespace string) (bool, error) {
		asks++
		return false, nil
	})

	post := func(path, body string, withCredential bool) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if withCredential {
			req.Header.Set("Authorization", "Bearer tok")
		}
		rec := httptest.NewRecorder()
		ctx := web.NewContext()
		ctx.Reset(rec, req)
		ctx.Input.CopyBody(1 << 20)
		BalanceGateFilter(ctx)
		return rec.Code
	}

	// A caller arriving at the public lane carrying a credential — the signed-in
	// reader — must not be held to their own ceiling by this gate.
	if code := post("/v1/chat/public", `{"model":"enso-free","messages":[]}`, true); code == http.StatusPaymentRequired {
		t.Error("the balance gate refused the public lane; the lane bills nobody and must not 402 here")
	}
	if asks != 0 {
		t.Fatalf("the balance gate read %d allowance(s) on /v1/chat/public; it would be reading the wrong subject's", asks)
	}

	// A body naming a PRICED model must not steer the gate either: the lane ignores
	// the model, so nothing here may act on it.
	if code := post("/v1/chat/public", `{"model":"claude-opus-5","messages":[]}`, true); code != http.StatusOK && code != 0 {
		t.Errorf("a priced model named on the public lane drove the gate to %d; the lane ignores the model", code)
	}
	if asks != 0 {
		t.Fatalf("asks = %d after a priced-model body on the public lane, want 0", asks)
	}

	// The CREDENTIALED surface is untouched: it is held to exactly one ceiling, its own.
	if code := post("/v1/chat/completions", `{"model":"enso-free","messages":[]}`, true); code == http.StatusPaymentRequired {
		t.Error("the free route on the credentialed surface was refused with allowance left")
	}
	if asks != 1 {
		t.Fatalf("/v1/chat/completions read the allowance %d time(s), want exactly 1 — one request, one ceiling", asks)
	}
}
