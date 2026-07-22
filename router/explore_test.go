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

package router

import (
	"context"
	"math/rand"
	"testing"
)

func exploreClient(explore float64, arms ...string) Client {
	return Client{
		Policy:  Policy{Prefer: map[string][]string{DefaultTaskKey: arms}},
		Explore: explore,
		Rand:    rand.New(rand.NewSource(1)),
	}
}

// TestExploreSamplesNonChampion: with Explore=1 the router always samples a
// non-champion servable arm (Source=explore), and over many rolls covers every
// alternative — so the bandit collects reward on arms other than the champion.
func TestExploreSamplesNonChampion(t *testing.T) {
	c := exploreClient(1.0, "champ", "alt1", "alt2")
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		d := c.RouteDecision(context.Background(), Request{}, Slo{})
		if d.Model == "champ" {
			t.Fatalf("explore returned the champion")
		}
		if d.Source != SourceExplore {
			t.Fatalf("source = %q, want %q", d.Source, SourceExplore)
		}
		seen[d.Model] = true
	}
	if !seen["alt1"] || !seen["alt2"] {
		t.Fatalf("explore did not cover both alternatives: %v", seen)
	}
}

// TestExploreZeroIsPureExploit: unset epsilon never explores — the champion serves,
// Source=heuristic. This is the safe default (behavior unchanged unless enabled).
func TestExploreZeroIsPureExploit(t *testing.T) {
	c := exploreClient(0, "champ", "alt1")
	d := c.RouteDecision(context.Background(), Request{}, Slo{})
	if d.Model != "champ" || d.Source != SourceHeuristic {
		t.Fatalf("Explore=0 must exploit: got %s/%s, want champ/heuristic", d.Model, d.Source)
	}
}

// TestExploreSingleArmExploits: with only one servable arm there is nothing to
// explore toward, so it exploits rather than dead-ending.
func TestExploreSingleArmExploits(t *testing.T) {
	c := exploreClient(1.0, "only")
	d := c.RouteDecision(context.Background(), Request{}, Slo{})
	if d.Model != "only" {
		t.Fatalf("single-arm explore must exploit, got %q", d.Model)
	}
}

// TestExploreRateIsApproximatelyEpsilon: over many rolls the explore fraction tracks
// epsilon (not 0, not 1) — a genuine floor, not a collapse to either extreme.
func TestExploreRateIsApproximatelyEpsilon(t *testing.T) {
	c := exploreClient(0.2, "champ", "alt")
	explored := 0
	const n = 4000
	for i := 0; i < n; i++ {
		if c.RouteDecision(context.Background(), Request{}, Slo{}).Source == SourceExplore {
			explored++
		}
	}
	rate := float64(explored) / n
	if rate < 0.15 || rate > 0.25 {
		t.Fatalf("explore rate %.3f not ~0.2", rate)
	}
}

// TestExploreStaysServableUnderPolicy: an org may ENABLE a model that the process
// cannot currently SERVE (e.g. its provider was disconnected). Exploration must
// never sample such an enabled-but-unserved arm — doing so 404s the request. Here
// the org enables {served-champ, unserved-alt} but the process serves only
// served-champ, so with Explore=1 the router must stay on the served set (exploit
// the lone served arm) rather than dead-end on the unservable explore pick.
// Pre-fix (ec.Known = rp.Enabled) this sampled "unserved-alt" and 404'd.
func TestExploreStaysServableUnderPolicy(t *testing.T) {
	c := Client{
		Policy:  Policy{Prefer: map[string][]string{DefaultTaskKey: {"served-champ", "unserved-alt"}}},
		Known:   func(m string) bool { return m == "served-champ" }, // process serves only champ
		Explore: 1.0,
		Rand:    rand.New(rand.NewSource(1)),
	}
	rp := RoutingPolicy{Enabled: rpEnabled("served-champ", "unserved-alt")} // org enabled both
	for i := 0; i < 200; i++ {
		d := c.RouteDecisionFor(context.Background(), Request{Text: "hi"}, Slo{}, rp)
		if d.Model == "unserved-alt" {
			t.Fatalf("exploration routed to an enabled-but-UNSERVED model (would 404): %+v", d)
		}
		if d.Model != "served-champ" {
			t.Fatalf("expected the served champ, got %+v", d)
		}
	}
}

// TestExploreSamplesOnlyServedAlternatives: with multiple SERVED arms plus an
// enabled-but-UNSERVED arm, exploration must cover the served alternatives yet
// never sample the unserved one. Proves the fix filters the explore pool by
// servability (served ∩ enabled), not merely by the org allowlist.
func TestExploreSamplesOnlyServedAlternatives(t *testing.T) {
	c := Client{
		Policy:  Policy{Prefer: map[string][]string{DefaultTaskKey: {"champ", "alt", "unserved"}}},
		Known:   func(m string) bool { return m != "unserved" }, // process serves champ + alt
		Explore: 1.0,
		Rand:    rand.New(rand.NewSource(1)),
	}
	rp := RoutingPolicy{Enabled: rpEnabled("champ", "alt", "unserved")} // org enabled all three
	seenAlt := false
	for i := 0; i < 200; i++ {
		d := c.RouteDecisionFor(context.Background(), Request{Text: "hi"}, Slo{}, rp)
		if d.Model == "unserved" {
			t.Fatalf("explore sampled the enabled-but-unserved arm: %+v", d)
		}
		if d.Source == SourceExplore && d.Model == "alt" {
			seenAlt = true
		}
	}
	if !seenAlt {
		t.Fatalf("explore never sampled the served alternative 'alt'")
	}
}
