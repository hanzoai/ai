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
	"testing"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

func TestBalanceGateCanonicalizesBareFable52BeforeLimits(t *testing.T) {
	gateWith(t, 100000)
	var asked []object.LimitAsk
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked = append(asked, q)
		return &object.LimitGrant{Plan: "pro", Pays: object.PaysPlan, Class: object.ClassPremium, Settle: func(int64) {}}, nil, nil
	})
	for _, model := range []string{"claude-fable-5.2", "claude-fable-5-2"} {
		chatWith(model, "/v1/chat/completions")
	}
	if len(asked) != 2 {
		t.Fatalf("asked %d times, want 2", len(asked))
	}
	for i, q := range asked {
		if q.Model != "anthropic/claude-fable-5.2" {
			t.Errorf("ask[%d].Model = %q, want canonical anthropic/claude-fable-5.2 (bare aliases must not bypass TopPremium)", i, q.Model)
		}
	}
}

func TestBalanceGateForwardsXHanzoZDR(t *testing.T) {
	gateWith(t, 100000)
	object.SetLimits(func(_ stdcontext.Context, _ object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		return &object.LimitGrant{Plan: "pro", Pays: object.PaysPlan, Class: object.ClassPremium, Settle: func(int64) {}}, nil, nil
	})
	for _, hdrVal := range []string{"1", "true"} {
		var forwardedZDR string
		p := ask(http.MethodPost, "/v1/messages").
			with("Authorization", "Bearer tok").
			with("X-Hanzo-ZDR", hdrVal).
			body([]byte(`{"model":"anthropic/claude-sonnet-5.5","messages":[{"role":"user","content":"hi"}]}`)).
			through(BalanceGateFilter, func(c *zip.Ctx) error {
				forwardedZDR = c.Header("X-Hanzo-ZDR")
				return c.Continue()
			})
		if p.status() != http.StatusOK {
			t.Fatalf("ZDR=%q status=%d (%s)", hdrVal, p.status(), p.said())
		}
		if forwardedZDR != "1" {
			t.Errorf("ZDR=%q forwarded X-Hanzo-ZDR = %q, want 1", hdrVal, forwardedZDR)
		}
		if !controllers.ZDRFromContext(p.left()) {
			t.Errorf("ZDR=%q did not set ZDRFromContext", hdrVal)
		}
	}
}
