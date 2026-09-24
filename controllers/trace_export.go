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

package controllers

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ServedUsage is the trace input for a request that was BILLED ELSEWHERE — a
// co-resident subsystem that debits commerce itself (zen's Meter in the unified
// cloud binary) but must still land in the ONE usage warehouse + o11y span
// plane this package owns. Money is exact nano-USD when the biller knows it
// (zen computes both retail and upstream COGS per served tier); 0 falls back to
// the rate-table recompute.
type ServedUsage struct {
	Owner            string // billing org (who paid) — the warehouse partition key
	User             string // actor, "owner/name" when known
	Model            string // the SKU the caller requested (zen5, …)
	Provider         string // serving family label ("zen")
	RequestID        string
	Status           string // "success" | "error" | "failover" (an arm that failed)
	ErrorMsg         string
	PromptTokens     int // the WHOLE prompt; CachedTokens is a part of it
	CompletionTokens int
	BilledNano       int64 // exact retail billed, nano-USD; 0 = recompute from rates
	CostNano         int64 // exact upstream COGS, nano-USD; 0 = recompute from rates
	StartTime        time.Time

	Served          string        // the arm that generated the answer (gen_ai.response.model)
	Vendor          string        // the provider that ran that arm (gen_ai.provider.name)
	Failover        string        // the arms that failed before it, with why
	CachedTokens    int           // of PromptTokens: served from the upstream's prompt cache
	ReasoningTokens int           // of CompletionTokens: spent reasoning
	First           time.Duration // time from StartTime to the first token of the answer
}

// TraceServedUsage emits the warehouse row (hanzo.cloud_usage) + gen_ai span for
// a self-billed request — recordTrace WITHOUT recordUsage, so the commerce debit
// the caller already made is never doubled. One warehouse writer, two billers,
// zero double-billing. Safe under a nil/zero input: an empty Owner still writes
// an (unattributed) row so traffic is never silently invisible; StartTime zero
// anchors at now.
func TraceServedUsage(ctx context.Context, in ServedUsage) {
	if in.StartTime.IsZero() {
		in.StartTime = time.Now().UTC()
	}
	if in.RequestID == "" {
		in.RequestID = uuid.NewString()
	}
	recordTrace(ctx, servedRecord(in), in.StartTime)
}

// servedRecord projects a ServedUsage onto the usage record recordTrace writes.
func servedRecord(in ServedUsage) *usageRecord {
	// The record counts the prompt the way the family path does: PromptTokens is
	// the part read fresh and CacheReadTokens the cached part beside it.
	cached := min(max(in.CachedTokens, 0), max(in.PromptTokens, 0))
	return &usageRecord{
		Owner:            in.Owner,
		Organization:     in.Owner,
		User:             in.User,
		Model:            in.Model,
		Provider:         in.Provider,
		PromptTokens:     max(in.PromptTokens-cached, 0),
		CacheReadTokens:  cached,
		CompletionTokens: in.CompletionTokens,
		TotalTokens:      in.PromptTokens + in.CompletionTokens,
		Served:           in.Served,
		Vendor:           in.Vendor,
		Failover:         in.Failover,
		ReasoningTokens:  in.ReasoningTokens,
		First:            in.First,
		Currency:         "USD",
		Status:           in.Status,
		ErrorMsg:         in.ErrorMsg,
		RequestID:        in.RequestID,
		BilledNanoExact:  &in.BilledNano,
		CostNanoExact:    &in.CostNano,
	}
}
