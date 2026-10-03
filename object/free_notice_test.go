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

package object

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// A spent day is 429 insufficient_quota allowance_spent, and the sentence is the
// rule, word for word: the ceiling from the standing, the 00:00 UTC reset, the way
// on. The pay page rides beside it as upgrade_url, never inside it.
func TestAllowanceSpentStatesTheDay(t *testing.T) {
	midnight := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	n := AllowanceSpent("api.hanzo.ai", "acme", Standing{Spent: true, Window: "day", Limit: 50, Used: 50, Resets: midnight})

	if n.Status != http.StatusTooManyRequests || n.Type != "insufficient_quota" || n.Code != CodeAllowanceSpent {
		t.Fatalf("status=%d type=%q code=%q, want 429 insufficient_quota %s", n.Status, n.Type, n.Code, CodeAllowanceSpent)
	}
	if want := "Free tier: 50 calls per day. Resets at 00:00 UTC. Add credits to use paid models."; n.Message != want {
		t.Fatalf("message %q, want %q", n.Message, want)
	}

	var body struct {
		Error struct {
			Message    string `json:"message"`
			Code       string `json:"code"`
			ResetsAt   string `json:"resets_at"`
			UpgradeURL string `json:"upgrade_url"`
		} `json:"error"`
	}
	if err := json.Unmarshal(n.ErrorJSON(), &body); err != nil {
		t.Fatalf("ErrorJSON: %v", err)
	}
	if pay := PayURL("api.hanzo.ai", "acme"); pay == "" || body.Error.UpgradeURL != pay {
		t.Errorf("upgrade_url = %q, want the pay page %q", body.Error.UpgradeURL, pay)
	}
	if body.Error.ResetsAt != "2026-10-04T00:00:00Z" || body.Error.Message != n.Message {
		t.Errorf("body %+v", body.Error)
	}
}

// Any other window names its own reset, in UTC whatever zone the standing carries.
func TestAllowanceSpentNamesAnotherWindowsReset(t *testing.T) {
	resets := time.Date(2026, 10, 3, 14, 30, 0, 0, time.FixedZone("PDT", -7*3600)) // 21:30 UTC
	n := AllowanceSpent("api.hanzo.ai", "acme", Standing{Spent: true, Window: "hour", Limit: 10, Used: 10, Resets: resets})
	if want := "Free tier: 10 calls per hour. Resets at 21:30 UTC. Add credits to use paid models."; n.Message != want {
		t.Fatalf("message %q, want %q", n.Message, want)
	}
	if n.Status != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", n.Status)
	}
}
