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
