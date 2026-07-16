// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import (
	"testing"
	"time"

	"github.com/hanzoai/dbx"
)

// TestRoutingEventPersistAndFilter exercises AddRoutingEvent + GetRoutingEvents
// org/since filtering against a live DB. Like every object test it initializes
// the adapter (requires the CI DB service); it is skipped when the adapter is
// unavailable so a DB-less run does not panic here.
func TestRoutingEventPersistAndFilter(t *testing.T) {
	if adapter == nil || adapter.db == nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Skipf("no DB adapter available: %v", r)
				}
			}()
			InitConfig()
		}()
	}
	if adapter == nil || adapter.db == nil {
		t.Skip("no DB adapter available")
	}
	if err := adapter.db.Sync(&RoutingEvent{}); err != nil {
		t.Fatalf("sync routing_event: %v", err)
	}

	t0 := time.Now().UTC()
	mk := func(id, org, user, task, routed string, at time.Time) *RoutingEvent {
		return &RoutingEvent{
			Id: id, CreatedTime: at.Format(time.RFC3339), Owner: org, User: user,
			Task: task, RequestedModel: "auto", RoutedModel: routed, Source: "heuristic",
		}
	}

	// Two orgs, two timestamps.
	old := t0.Add(-2 * time.Hour)
	recent := t0.Add(-1 * time.Minute)
	seed := []*RoutingEvent{
		mk("re-acme-old", "re-acme", "re-acme/a", "code", "zen4-coder", old),
		mk("re-acme-new", "re-acme", "re-acme/a", "general", "zen4", recent),
		mk("re-beta-new", "re-beta", "re-beta/b", "math", "zen4-ultra", recent),
	}
	for _, e := range seed {
		if err := AddRoutingEvent(e); err != nil {
			t.Fatalf("AddRoutingEvent(%s): %v", e.Id, err)
		}
	}
	t.Cleanup(func() {
		for _, e := range seed {
			_, _ = deleteByPK(adapter.db, "routing_event", dbx.HashExp{"id": e.Id})
		}
	})

	// org filter isolates a tenant.
	acme, err := GetRoutingEvents("re-acme", "")
	if err != nil {
		t.Fatalf("GetRoutingEvents(org): %v", err)
	}
	if n := countSeed(acme, "re-acme"); n != 2 {
		t.Errorf("org filter returned %d re-acme events, want 2", n)
	}
	if n := countSeed(acme, "re-beta"); n != 0 {
		t.Errorf("org filter leaked %d re-beta events, want 0", n)
	}

	// since filter drops the old event.
	sinceCut := t0.Add(-30 * time.Minute).Format(time.RFC3339)
	recentAcme, err := GetRoutingEvents("re-acme", sinceCut)
	if err != nil {
		t.Fatalf("GetRoutingEvents(org, since): %v", err)
	}
	if n := countSeed(recentAcme, "re-acme"); n != 1 {
		t.Errorf("since filter returned %d recent re-acme events, want 1", n)
	}

	// org+since across tenants: only the recent beta event, none of acme.
	recentBeta, err := GetRoutingEvents("re-beta", sinceCut)
	if err != nil {
		t.Fatalf("GetRoutingEvents(beta, since): %v", err)
	}
	if n := countSeed(recentBeta, "re-beta"); n != 1 {
		t.Errorf("beta since filter returned %d events, want 1", n)
	}
}

// countSeed counts events belonging to the given seed org (ignores any unrelated
// rows a shared test DB may already hold).
func countSeed(events []*RoutingEvent, org string) int {
	n := 0
	for _, e := range events {
		if e.Owner == org {
			n++
		}
	}
	return n
}

// findByRequest returns the event carrying the given request_id, or nil.
func findByRequest(events []*RoutingEvent, requestId string) *RoutingEvent {
	for _, e := range events {
		if e.RequestId == requestId {
			return e
		}
	}
	return nil
}

// TestRoutingRewardAttachAndExport exercises the reward signal end-to-end against
// the canonical in-memory SQLite store (the real query path, same driver prod
// uses): AttachRoutingReward finds an event by (org, request_id) and stamps
// reward+rewarded_time, rejects an unknown request_id (the endpoint's 404 path) and
// a cross-org request_id (found=false), and overwrites idempotently; then
// GetRewardedRoutingEvents yields only the rewarded, org-scoped tuples.
func TestRoutingRewardAttachAndExport(t *testing.T) {
	restore, err := UseMemoryDB("file:routingreward?mode=memory&cache=shared", &RoutingEvent{})
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)

	now := time.Now().UTC().Format(time.RFC3339)
	seed := []*RoutingEvent{
		{
			Id: "rw-acme-1", CreatedTime: now, Owner: "rw-acme", User: "rw-acme/a", RequestId: "req-acme-1",
			Task: "code", RequestedModel: "auto", RoutedModel: "glm-5.2", Source: "engine", Features: "[0.1,0.2]",
		},
		{
			Id: "rw-acme-2", CreatedTime: now, Owner: "rw-acme", User: "rw-acme/a", RequestId: "req-acme-2",
			Task: "general", RequestedModel: "auto", RoutedModel: "glm-5.2-mini", Source: "heuristic",
		},
		{
			Id: "rw-beta-1", CreatedTime: now, Owner: "rw-beta", User: "rw-beta/b", RequestId: "req-beta-1",
			Task: "math", RequestedModel: "auto", RoutedModel: "glm-5.2", Source: "engine", Features: "[0.3]",
		},
	}
	for _, e := range seed {
		if err := AddRoutingEvent(e); err != nil {
			t.Fatalf("AddRoutingEvent(%s): %v", e.Id, err)
		}
	}

	// Attach to a known (org, request_id).
	if found, err := AttachRoutingReward("rw-acme", "req-acme-1", 0.75); err != nil || !found {
		t.Fatalf("AttachRoutingReward(known) = (%v, %v), want (true, nil)", found, err)
	}
	// Unknown request_id → not found (the endpoint's 404 path).
	if found, err := AttachRoutingReward("rw-acme", "req-nope", 0.5); err != nil || found {
		t.Fatalf("AttachRoutingReward(unknown) = (%v, %v), want (false, nil)", found, err)
	}
	// Cross-org: beta's request under acme must not match (rejection, no write).
	if found, err := AttachRoutingReward("rw-acme", "req-beta-1", 0.5); err != nil || found {
		t.Fatalf("AttachRoutingReward(cross-org) = (%v, %v), want (false, nil)", found, err)
	}
	// Idempotent overwrite: a repeat replaces the reward.
	if found, err := AttachRoutingReward("rw-acme", "req-acme-1", 0.25); err != nil || !found {
		t.Fatalf("AttachRoutingReward(overwrite) = (%v, %v), want (true, nil)", found, err)
	}

	// Read back: only the rewarded acme event, carrying the OVERWRITTEN reward + a time.
	rewarded, err := GetRewardedRoutingEvents("rw-acme", "")
	if err != nil {
		t.Fatalf("GetRewardedRoutingEvents: %v", err)
	}
	if n := countSeed(rewarded, "rw-acme"); n != 1 {
		t.Fatalf("rewarded acme events = %d, want 1 (only req-acme-1)", n)
	}
	got := findByRequest(rewarded, "req-acme-1")
	if got == nil {
		t.Fatal("rewarded export missing req-acme-1")
	}
	if got.Reward != 0.25 {
		t.Errorf("reward = %v, want 0.25 (overwritten)", got.Reward)
	}
	if got.RewardedTime == "" {
		t.Error("rewarded_time empty, want set")
	}
	// The featureful decision keeps its feature vector for training.
	if got.Features != "[0.1,0.2]" {
		t.Errorf("features = %q, want [0.1,0.2]", got.Features)
	}

	// Cross-org isolation on the read side: beta has no rewarded events.
	if beta, err := GetRewardedRoutingEvents("rw-beta", ""); err != nil || countSeed(beta, "rw-beta") != 0 {
		t.Fatalf("GetRewardedRoutingEvents(beta) = (%d, %v), want (0, nil)", countSeed(beta, "rw-beta"), err)
	}
}
