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
	"encoding/json"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestRecordUsageDebitsCallerOrg proves a usage debit lands in the caller's own
// org namespace (X-Hanzo-Org) and keys the per-subject billing account — the
// debit half of per-org isolation. The org comes from the validated identity
// (record.Owner), never a client header, so it is inherently spoof-proof.
func TestRecordUsageDebitsCallerOrg(t *testing.T) {
	spy := startBillingSpy(t)

	recordUsage(&usageRecord{
		Owner:        "acme",
		User:         "acme/alice",
		Organization: "acme",
		Model:        "gpt-4o",
		Provider:     "do-ai",
		TotalTokens:  10,
		Status:       "success",
		RequestID:    "req-1",
	})

	recs := spy.drain()
	if len(recs) != 1 {
		t.Fatalf("expected exactly 1 usage debit, got %d", len(recs))
	}
	if recs[0].org != "acme" {
		t.Errorf("debit X-Hanzo-Org=%q, want acme (caller org)", recs[0].org)
	}
	var payload struct {
		User string `json:"user"`
	}
	if err := json.Unmarshal([]byte(recs[0].body), &payload); err != nil {
		t.Fatalf("debit payload not JSON: %v", err)
	}
	if want := object.BillingSubjectFromUserKey("acme", "acme/alice"); payload.User != want {
		t.Errorf("debit subject=%q, want %q", payload.User, want)
	}
}

// TestRecordUsageCrossOrgNoLeak fires debits for two orgs and asserts each is
// scoped to its own namespace — org A's spend never lands in org B's account.
func TestRecordUsageCrossOrgNoLeak(t *testing.T) {
	spy := startBillingSpy(t)

	recordUsage(&usageRecord{Owner: "org-a", User: "org-a/a", Model: "gpt-4o", TotalTokens: 5, Status: "success", RequestID: "a"})
	recordUsage(&usageRecord{Owner: "org-b", User: "org-b/b", Model: "gpt-4o", TotalTokens: 5, Status: "success", RequestID: "b"})

	recs := spy.drain()
	byOrg := map[string]int{}
	for _, r := range recs {
		byOrg[r.org]++
	}
	if byOrg["org-a"] != 1 || byOrg["org-b"] != 1 {
		t.Fatalf("per-org debit counts wrong: %v (want org-a:1 org-b:1)", byOrg)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 debits total, got %d (leak?)", len(recs))
	}
}

// TestRecordUsageSkipsNonSuccess is the fail-secure billing rule: only a
// successful call is debited. An error/empty status must emit NOTHING, so a
// failed request can never be charged.
func TestRecordUsageSkipsNonSuccess(t *testing.T) {
	spy := startBillingSpy(t)

	recordUsage(&usageRecord{Owner: "acme", User: "acme/a", Model: "gpt-4o", Status: "error", ErrorMsg: "upstream 500", RequestID: "e"})
	recordUsage(&usageRecord{Owner: "acme", User: "acme/a", Model: "gpt-4o", Status: "", RequestID: "blank"})

	if recs := spy.drain(); len(recs) != 0 {
		t.Fatalf("non-success debit emitted %d record(s); must be 0: %+v", len(recs), recs)
	}
}

// TestRecordUsageNilQueueIsNoop asserts metering is a safe no-op when Commerce
// is not configured (queue nil) — never a panic on the hot path.
func TestRecordUsageNilQueueIsNoop(t *testing.T) {
	billingQueue = nil
	recordUsage(&usageRecord{Owner: "acme", User: "acme/a", Model: "gpt-4o", Status: "success", RequestID: "x"})
	// No panic == pass.
}
