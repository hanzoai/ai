// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
)

// fastWidth is how many providers a fast request is offered to at once.
//
// TWO, not three. The tail this cuts is one provider drawing a slow response
// while another would have answered normally, and a second opinion removes most
// of it; a third removes a little more for another whole completion. The cost is
// linear in this number and the benefit is not, so it starts where the trade is
// clearly worth it.
const fastWidth = 2

// fastFlag is the ask as the BODY carries it, for the same reason retrieval is:
// a browser preflights custom headers and the edge's CORS allow-list names only
// the standard ones, so a public page cannot send X-Fast. The header form stays
// for server-side callers; either spelling works.
type fastFlag struct {
	Fast bool `json:"fast"`
}

// wantsFast reports whether this request asked for fast mode.
//
// Asking is not getting. What it costs is settled where the money is —
// widthFor — because the answer depends on whether the caller can be billed for
// what the extra attempts spend.
func (c *ApiController) wantsFast() bool {
	if v := c.Header("X-Fast"); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	var f fastFlag
	_ = json.Unmarshal(c.Body(), &f)
	return f.Fast
}

// widthFor answers how many providers this request may be offered to at once,
// and reserves for all of them.
//
// FAST MODE IS AN OPTIMISATION, NOT AN ENTITLEMENT. A caller who asks for it and
// cannot cover N completions gets the ordinary one rather than a refusal: they
// asked to go faster, not to be turned away, and refusing here would invent a
// second way for a funded account to be told no.
//
// A caller nobody can be billed for never gets it at all. The public lane has no
// reservation to make, so hedging there would spend N completions on a stranger
// and settle none.
func (c *ApiController) widthFor(user *iam.User, subject string, est int64) (*budgetHold, int, bool) {
	if user != nil && c.wantsFast() {
		if hold, ok := reserveBudget(subject, est*fastWidth); ok {
			return hold, fastWidth, true
		}
	}
	hold, ok := reserveBudget(subject, est)
	return hold, 1, ok
}

// billRaced is what fan.bill calls for each provider beaten in a race: one real
// usage row for real tokens, because a cancelled attempt was charged to us by
// the vendor whether or not its answer was ever read.
//
// EVERY REQUEST FIELD IS READ NOW, not inside the closure. By the time this
// fires the handler has returned and fasthttp has recycled the request, so
// reading the IP there reads whatever the next caller put in its place. The
// context is Background for the same reason: the request's own is cancelled the
// moment the response ends, which is before most losers have finished.
func (c *ApiController) billRaced(model string, user *iam.User, premium, stream bool,
	requestId string, start time.Time,
) func(attempt) {
	if user == nil {
		return nil
	}
	owner, ip := c.billingOrg(user), c.Fiber().IP()
	ctx := context.Background()
	return func(a attempt) {
		rec := &usageRecord{
			Owner:            owner,
			Organization:     user.Owner,
			Model:            model,
			Provider:         a.provider,
			Origin:           a.origin,
			PromptTokens:     a.prompt,
			CompletionTokens: a.completion,
			TotalTokens:      a.prompt + a.completion,
			Currency:         "USD",
			Premium:          premium,
			Stream:           stream,
			Status:           "hedged",
			ErrorMsg:         a.err.Error(),
			ClientIP:         ip,
			RequestID:        requestId,
		}
		rec.bind(ctx, user)
		// Whose key paid decides what this costs, and for a beaten provider that
		// is ITS row — not the one auth resolved before the race started.
		rec.BYO, rec.Account = providerBYO(a.row, user)
		// A vendor that refused before producing anything spent nothing, and the
		// row still goes to the trace so the refusal is visible.
		if a.prompt > 0 || a.completion > 0 {
			recordUsage(rec)
		}
		recordTrace(ctx, rec, start)
	}
}
