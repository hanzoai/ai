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
	"fmt"
	"net/http"
	"time"
)

// The Free plan is limited usage served from ONE pool every free user shares: the
// platform's vendor accounts for free models, spent in turn. It refuses three ways,
// and each says which, when it clears, and where to upgrade:
//
//	allowance_spent  429  this person's free calls for the day are used
//	pool_busy        429  the shared pool is at its vendor limit for a minute
//	pool_exhausted   429  the shared pool is spent until the vendor resets it
//
// All three are 429 because none is about money owed: the call was well formed and
// asked too often, and each clears by waiting. The code says which wait it is.
//
// Clients switch on the code, never on the sentence.
const (
	CodeAllowanceSpent = "allowance_spent"
	CodePoolBusy       = "pool_busy"
	CodePoolExhausted  = "pool_exhausted"
)

// FreeNotice is one Free-plan refusal: the sentence, the code, the status, when
// the thing that refused starts again, and the page that lifts it.
type FreeNotice struct {
	Message string
	Code    string
	Type    string
	Status  int
	Resets  time.Time
	Upgrade string
}

// AllowanceSpent is the refusal for a person whose free calls for the window are
// used: fifty a UTC day on the Free plan, counted by the host as each was admitted.
//
// THE SENTENCE IS THE RULE, stated once and the same everywhere it is refused: the
// ceiling, when it reopens, and the way on. The day always reopens at 00:00 UTC, so
// it says so in those words; any other window names its own reset. The pay page is
// not in the sentence — it rides beside it as upgrade_url (ErrorJSON), where a
// client can link it without parsing English.
func AllowanceSpent(host, org string, s Standing) FreeNotice {
	msg := fmt.Sprintf("Free tier: %d calls per day. Resets at 00:00 UTC. Add credits to use paid models.", s.Limit)
	if s.Window != "day" {
		msg = fmt.Sprintf("Free tier: %d calls per %s. Resets at %s UTC. Add credits to use paid models.",
			s.Limit, s.Window, s.Resets.UTC().Format("15:04"))
	}
	return FreeNotice{
		Message: msg,
		Code:    CodeAllowanceSpent,
		Type:    "insufficient_quota",
		Status:  http.StatusTooManyRequests,
		Resets:  s.Resets,
		Upgrade: PayURL(host, org),
	}
}

// PoolRefused is the refusal for a free request when the pool every free user
// shares has no account left to serve it: busy for a minute, or spent until the
// vendor resets it.
//
// A BUSY POOL CARRIES NO LATE RESET. Busy says "a minute"; a reset hours away
// belongs to an account that is spent for the day while others still serve, and
// sent as Retry-After it would park a client for hours on a pool that answers
// again in seconds. Only exhausted states when it refills.
//
// paid says the caller's own plan is paid: they reached the free models, and the
// way on is a paid model, not an upgrade they already have.
func PoolRefused(host, org, state string, resets time.Time, paid bool) FreeNotice {
	pay := PayURL(host, org)
	n := FreeNotice{Type: "rate_limit_error", Status: http.StatusTooManyRequests}
	if !paid {
		n.Upgrade = pay
	}
	if state == "exhausted" {
		n.Resets = resets
		when := "for now"
		if !resets.IsZero() {
			when = "until " + clock(resets)
		}
		n.Code = CodePoolExhausted
		if paid {
			n.Message = fmt.Sprintf("The free models are served from a pool shared by all free users, and it is used up %s. Pick a paid model to keep going.", when)
			return n
		}
		n.Message = fmt.Sprintf("The Free plan is limited usage from a pool shared by all free users, and the pool is used up %s. Upgrade at %s to keep going now.",
			when, pay)
		return n
	}
	if !resets.IsZero() && time.Until(resets) <= time.Minute {
		n.Resets = resets
	}
	n.Code = CodePoolBusy
	if paid {
		n.Message = "The free models are served from a pool shared by all free users, and it is busy right now. Try again in a minute, or pick a paid model to keep going."
		return n
	}
	n.Message = fmt.Sprintf("The Free plan is limited usage from a pool shared by all free users, and the pool is busy right now. Try again in a minute, or upgrade at %s to keep going now.",
		pay)
	return n
}

// clock is an instant as a person reads a reset: the UTC time of day, with the
// date when it is not today.
func clock(t time.Time) string {
	u := t.UTC()
	if n := time.Now().UTC(); u.Year() == n.Year() && u.YearDay() == n.YearDay() {
		return u.Format("15:04 UTC")
	}
	return u.Format("Jan 2 15:04 UTC")
}

// RetryAfter is how many whole seconds from now the refusal clears, 0 when it
// does not say.
func (n FreeNotice) RetryAfter(now time.Time) int64 {
	if n.Resets.IsZero() || !n.Resets.After(now) {
		return 0
	}
	return int64(n.Resets.Sub(now).Seconds() + 0.999)
}

// ErrorJSON renders the refusal in the OpenAI error envelope, with the reset and
// the upgrade page beside the code so a client can show both without parsing the
// sentence.
func (n FreeNotice) ErrorJSON() []byte {
	type body struct {
		Message    string `json:"message"`
		Type       string `json:"type"`
		Code       string `json:"code"`
		ResetsAt   string `json:"resets_at,omitempty"`
		UpgradeURL string `json:"upgrade_url,omitempty"`
	}
	b := body{Message: n.Message, Type: n.Type, Code: n.Code, UpgradeURL: n.Upgrade}
	if !n.Resets.IsZero() {
		b.ResetsAt = n.Resets.UTC().Format(time.RFC3339)
	}
	out, _ := json.Marshal(struct {
		Error body `json:"error"`
	}{b})
	return out
}
