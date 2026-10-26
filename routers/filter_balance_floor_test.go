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
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
)

// floorChat sends model on path with an API key, as a stream that forces a tool and
// asks for JSON, so a test can see the floor is asked the same request.
func floorChat(model, path string) probe {
	body := `{"model":"` + model + `","stream":true,"max_tokens":1000,"response_format":{"type":"json_object"},` +
		`"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":{"type":"function","function":{"name":"f"}},` +
		`"messages":[{"role":"user","content":"hi"}]}`
	if path == "/v1/responses" {
		body = `{"model":"` + model + `","stream":true,"input":"hi"}`
	}
	return ask(http.MethodPost, path).with("Authorization", "Bearer tok").body([]byte(body)).through(BalanceGateFilter)
}

// handedModel is the model the request the handler received names, and its body.
func handedModel(t *testing.T, p probe) (string, map[string]json.RawMessage) {
	t.Helper()
	var b map[string]json.RawMessage
	_ = json.Unmarshal([]byte(p.handed()), &b)
	var m string
	_ = json.Unmarshal(b["model"], &m)
	return m, b
}

// fellTo checks a request was handed to its floor, on a 200, saying why.
func fellTo(t *testing.T, name string, p probe, floor, why string) {
	t.Helper()
	if p.status() != http.StatusOK {
		t.Fatalf("%s: status %d (%s), want the floor", name, p.status(), p.said())
	}
	if m, _ := handedModel(t, p); m != floor {
		t.Errorf("%s: handed %q, want %q", name, m, floor)
	}
	for h, want := range map[string]string{controllers.FallbackHeader: floor, controllers.FallbackReasonHeader: why} {
		if got := p.replied(h); got != want {
			t.Errorf("%s: %s = %q, want %q", name, h, got, want)
		}
	}
}

// AN EMPTY WALLET NEVER STOPS AN ENSO OR ZEN MODEL, whoever asks — an API key here,
// not only a person in an app — on every chat surface: the same request goes to the
// family's free model, and the answer says so. A funded wallet is served as asked.
func TestAnEmptyWalletFallsToTheFloorOnEveryChatSurface(t *testing.T) {
	freeModels(t, "zen-free", "enso-free")
	for model, floor := range map[string]string{"zen6": "zen-free", "enso-pro": "enso-free", "enso-ultra": "enso-free", "enso-flash": "enso-free"} {
		for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
			gateWith(t, 0)
			p := floorChat(model, path)
			fellTo(t, model+" "+path, p, floor, object.CodeInsufficientBalance)
			if p.replied("X-Hanzo-Usage") != "limited" || p.replied("X-Hanzo-Topup-Url") == "" {
				t.Errorf("%s %s: usage %q topup %q, want limited mode's way out", model, path, p.replied("X-Hanzo-Usage"), p.replied("X-Hanzo-Topup-Url"))
			}
			if _, b := handedModel(t, p); string(b["stream"]) != "true" {
				t.Errorf("%s %s: the floor was not asked to stream: %s", model, path, p.handed())
			} else if path == "/v1/chat/completions" && (string(b["tool_choice"]) == "" || string(b["response_format"]) != `{"type":"json_object"}`) {
				t.Errorf("%s: the floor was not asked the same request: %s", model, p.handed())
			}

			gateWith(t, 500)
			if p := floorChat(model, path); p.status() != http.StatusOK || p.replied(controllers.FallbackHeader) != "" {
				t.Errorf("%s %s funded: %d fallback %q", model, path, p.status(), p.replied(controllers.FallbackHeader))
			} else if m, _ := handedModel(t, p); m != model {
				t.Errorf("%s %s funded: handed %q", model, path, m)
			}
		}
	}
	// An embedding is no conversation, and somebody else's model has no floor.
	gateWith(t, 0)
	if p := ask(http.MethodPost, "/v1/embeddings").with("Authorization", "Bearer tok").
		body([]byte(`{"model":"zen-embedding","input":"hi"}`)).through(BalanceGateFilter); p.status() != http.StatusPaymentRequired {
		t.Errorf("an embedding at $0: %d, want 402", p.status())
	}
	if p := floorChat("anthropic/claude-opus-5.5", "/v1/chat/completions"); p.status() != http.StatusPaymentRequired || p.replied(controllers.FallbackHeader) != "" {
		t.Errorf("a third-party model at $0: %d fallback %q, want 402", p.status(), p.replied(controllers.FallbackHeader))
	}
}

// THE PLAN'S REFUSALS FALL TOO, and the floor is asked again from the top, so what
// the floor itself meets stands: its own cap, a spent window. A limit the caller set
// on its own key is never answered by another model. Nothing is asked a third time.
func TestAPlanRefusalFallsAndTheFloorsOwnRefusalStands(t *testing.T) {
	freeModels(t, "zen-free", "enso-free")
	freeGrant := &object.LimitGrant{Plan: "free", Pays: object.PaysFree, Class: object.ClassFree, Settle: func(int64) {}}
	for _, tc := range []struct {
		name        string
		asked, free *object.LimitHit
		status      int
		why         string
	}{
		{"no paid plan", &object.LimitHit{Code: object.CodePaidPlan}, nil, http.StatusOK, object.CodePaidPlan},
		{"the plan's usage used", &object.LimitHit{Code: object.CodePlanAllowance}, nil, http.StatusOK, object.CodePlanAllowance},
		{"the model's share used", &object.LimitHit{Code: object.CodeModelCap}, nil, http.StatusOK, object.CodeModelCap},
		{"the free plan's cap on the paid model", &object.LimitHit{Code: object.CodeFreePlanCap}, nil, http.StatusOK, object.CodeFreePlanCap},
		{"the floor's own cap", &object.LimitHit{Code: object.CodePaidPlan}, &object.LimitHit{Code: object.CodeFreePlanCap}, http.StatusTooManyRequests, ""},
		{"a spent window the floor meets too", &object.LimitHit{Code: object.CodeUsageCap, Name: "day"}, &object.LimitHit{Code: object.CodeUsageCap, Name: "day"}, http.StatusTooManyRequests, ""},
		{"the key's own budget", &object.LimitHit{Code: "key_budget", Name: "month"}, nil, http.StatusPaymentRequired, ""},
		{"a model the key may not use", &object.LimitHit{Code: "key_model", Name: "model"}, nil, http.StatusPaymentRequired, ""},
	} {
		gateWith(t, 0)
		var asked []string
		object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
			asked = append(asked, q.Model)
			if q.Model == "zen-free" {
				if tc.free != nil {
					return nil, tc.free, nil
				}
				return freeGrant, nil, nil
			}
			return nil, tc.asked, nil
		})
		p := floorChat("zen6", "/v1/chat/completions")
		if p.status() != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, p.status(), tc.status, p.said())
			continue
		}
		if tc.why != "" {
			fellTo(t, tc.name, p, "zen-free", tc.why)
		} else if p.handed() != "" {
			t.Errorf("%s: handed %s, want the refusal", tc.name, p.handed())
		}
		caller := tc.asked.Code == "key_budget" || tc.asked.Code == "key_model"
		if caller && (len(asked) != 1 || p.replied(controllers.FallbackHeader) != "") {
			t.Errorf("%s: asked %v fallback %q, want the key's refusal alone", tc.name, asked, p.replied(controllers.FallbackHeader))
		}
		if !caller && (len(asked) != 2 || asked[0] != "zen6" || asked[1] != "zen-free") {
			t.Errorf("%s: asked %v, want the model and then its floor, once each", tc.name, asked)
		}
	}
}

// A ROUTER ID HOLDS TO THE SAME RULE: a limit the caller set on its own key stands
// rather than being answered by the free SKU.
func TestARouterIdKeepsAKeysOwnLimit(t *testing.T) {
	prev := depthRoute
	depthRoute = func(model string, _ []byte) (string, string, bool) {
		if model != "enso-auto" {
			return "", "", false
		}
		return "enso-pro", "enso-free", true
	}
	t.Cleanup(func() { depthRoute = prev })
	paidSwitch(t)
	freeModels(t, "enso-free")
	gateWith(t, 500)
	var asked []string
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitGrant, *object.LimitHit, error) {
		asked = append(asked, q.Model)
		return nil, &object.LimitHit{Code: "key_budget", Name: "month"}, nil
	})
	p := floorChat("enso-auto", "/v1/chat/completions")
	if p.status() != http.StatusPaymentRequired || p.handed() != "" || p.replied(controllers.FallbackHeader) != "" {
		t.Fatalf("status %d handed %q fallback %q (%s), want the key's 402", p.status(), p.handed(), p.replied(controllers.FallbackHeader), p.said())
	}
	if len(asked) != 1 {
		t.Errorf("asked %v, want the tier alone", asked)
	}
}
