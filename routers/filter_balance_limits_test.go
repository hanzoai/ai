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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// TestPlanLimitsGate proves a priced call with money is refused at a plan limit with
// the window and its reset, admitted inside it, and refused (fail-closed) when the
// limit cannot be read.
func TestPlanLimitsGate(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/ann")
	bg.ledger.SetBalance("acme", 100000) // the balance admits; only the limit decides

	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetLimits(nil) })

	post := func() (int, string, string) {
		p := ask(http.MethodPost, "/v1/chat/completions")
		p = p.with("Authorization", "Bearer tok")
		p = p.body([]byte(`{"model":"vendor/priced","messages":[]}`))
		p = p.through(BalanceGateFilter)
		return p.status(), p.said(), p.replied("Retry-After")
	}

	reset := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	var asked object.LimitAsk
	object.SetLimits(func(_ stdcontext.Context, q object.LimitAsk) (*object.LimitHit, error) {
		asked = q
		return &object.LimitHit{Name: "session", ResetsAt: reset}, nil
	})
	code, body, retry := post()
	if code != http.StatusTooManyRequests {
		t.Fatalf("a call at a plan limit must be 429, got %d (%s)", code, body)
	}
	var e struct {
		Error struct {
			Code, Limit string
			ResetsAt    string `json:"resets_at"`
		}
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatal(err)
	}
	if e.Error.Code != "usage_cap_exceeded" || e.Error.Limit != "session" || e.Error.ResetsAt != reset.Format(time.RFC3339) {
		t.Errorf("429 names code=%q limit=%q resets_at=%q", e.Error.Code, e.Error.Limit, e.Error.ResetsAt)
	}
	if retry == "" {
		t.Error("429 carries no Retry-After")
	}
	if asked.Subject != "acme" || asked.Namespace != "acme" || asked.Actor != "acme/ann" || asked.Model != "vendor/priced" {
		t.Errorf("limits asked %+v", asked)
	}

	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitHit, error) { return nil, nil })
	if code, body, _ := post(); code == http.StatusTooManyRequests || code == http.StatusPaymentRequired || code == http.StatusServiceUnavailable {
		t.Errorf("a call inside every limit must be admitted, got %d (%s)", code, body)
	}

	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitHit, error) {
		return nil, errors.New("store unreadable")
	})
	if code, body, _ := post(); code != http.StatusServiceUnavailable {
		t.Errorf("an unreadable limit must refuse 503, got %d (%s)", code, body)
	}

	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitHit, error) {
		return &object.LimitHit{Name: "plan"}, nil
	})
	code, body, _ = post()
	var p struct {
		Error struct {
			Code       string
			UpgradeURL string `json:"upgrade_url"`
		}
	}
	_ = json.Unmarshal([]byte(body), &p)
	if code != http.StatusPaymentRequired || p.Error.Code != "plan_required" || !strings.HasSuffix(p.Error.UpgradeURL, "/cart?plan=dev") {
		t.Errorf("no plan and no credit must be 402 plan_required with the upgrade link, got %d (%s)", code, body)
	}

	object.SetLimits(nil)
	if code, body, _ := post(); code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable {
		t.Errorf("no limits installed must admit, got %d (%s)", code, body)
	}
}

// A priced call that names no model (crawl, ingest) is the balance gate's alone: the
// plan limits are never asked about it.
func TestPlanLimitsAreAskedOnlyForAModel(t *testing.T) {
	bg := newTestGate("http://unused", "", balanceCacheTTL)
	bg.setUserKeyCache("tok", "", "acme", "acme", "acme/ann")
	bg.ledger.SetBalance("acme", 100000)
	prev := balanceGate
	balanceGate = bg
	t.Cleanup(func() { balanceGate = prev })
	t.Cleanup(func() { object.SetLimits(nil) })

	asked := 0
	object.SetLimits(func(stdcontext.Context, object.LimitAsk) (*object.LimitHit, error) {
		asked++
		return &object.LimitHit{Name: "plan"}, nil
	})
	p := ask(http.MethodPost, "/v1/crawl").with("Authorization", "Bearer tok").body([]byte(`{"url":"https://example.com"}`)).through(BalanceGateFilter)
	if asked != 0 || p.status() == http.StatusPaymentRequired {
		t.Fatalf("a call naming no model asked the plan limits %d time(s), status %d", asked, p.status())
	}
}
