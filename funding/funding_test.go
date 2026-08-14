// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package funding

import (
	"testing"
	"time"
)

// TestRefuse pins every arm of the breaker. The cases that matter most are the
// ones that must NOT refuse: this guard sits in front of all paid inference, so a
// wrong true is an outage, not a saved dollar.
func TestRefuse(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    State
		want bool
	}{{
		name: "disarmed by default — a zero State never refuses",
		s:    State{},
		want: false,
	}, {
		name: "no ceiling set: over any spend, still allowed",
		s:    State{OnCash: true, TodayCents: 9_999_999},
		want: false,
	}, {
		name: "still on promo credit: ceiling does not apply",
		s:    State{OnCash: false, TodayCents: 500_00, CeilingCents: 200_00},
		want: false,
	}, {
		name: "on cash, under ceiling",
		s:    State{OnCash: true, TodayCents: 199_99, CeilingCents: 200_00},
		want: false,
	}, {
		name: "on cash, exactly at ceiling — refuse",
		s:    State{OnCash: true, TodayCents: 200_00, CeilingCents: 200_00},
		want: true,
	}, {
		name: "on cash, over ceiling — refuse",
		s:    State{OnCash: true, TodayCents: 250_00, CeilingCents: 200_00},
		want: true,
	}, {
		name: "negative ceiling is disarmed, not inverted",
		s:    State{OnCash: true, TodayCents: 1, CeilingCents: -1},
		want: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Refuse(); got != tc.want {
				t.Fatalf("Refuse() = %v, want %v (state %+v)", got, tc.want, tc.s)
			}
		})
	}
}

func TestRemainingCents(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    State
		want int64
	}{
		{"disarmed reports nothing left to report", State{}, 0},
		{"headroom", State{OnCash: true, TodayCents: 50_00, CeilingCents: 200_00}, 150_00},
		{"never negative", State{OnCash: true, TodayCents: 900_00, CeilingCents: 200_00}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.RemainingCents(); got != tc.want {
				t.Fatalf("RemainingCents() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestCurrentDefaultsToDisarmed is the safety property that lets this ship: with
// no publisher (standalone ai, or cloud failing to push), the breaker must be
// invisible. If this ever returns a refusing State, a missing telemetry push
// becomes a fleet-wide inference outage.
func TestCurrentDefaultsToDisarmed(t *testing.T) {
	current.Store(nil)
	if got := Current(); got.Refuse() {
		t.Fatalf("unpublished state refuses: %+v", got)
	}
}

func TestPublishRoundTrips(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })
	Publish(State{OnCash: true, TodayCents: 300_00, CeilingCents: 200_00})
	got := Current()
	if !got.Refuse() {
		t.Fatalf("published over-ceiling state does not refuse: %+v", got)
	}
	Publish(State{})
	if Current().Refuse() {
		t.Fatal("disarming publish did not take effect")
	}
}

// TestStale pins the aging rule on the value itself, including the two boundaries
// a wrong comparison would get backwards: a reading is good AT its Until, and a
// reading with no Until never ages.
func TestStale(t *testing.T) {
	at := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		s    State
		want bool
	}{
		{"no Until never goes stale", State{OnCash: true, CeilingCents: 1}, false},
		{"before Until", State{Until: at.Add(time.Second)}, false},
		{"exactly at Until is still good", State{Until: at}, false},
		{"after Until", State{Until: at.Add(-time.Second)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Stale(at); got != tc.want {
				t.Fatalf("Stale() = %v, want %v (state %+v at %v)", got, tc.want, tc.s, at)
			}
		})
	}
}

// TestCurrentDecaysWhenStale is the property that makes the ceiling SAFE to arm,
// and it is the one this package could not previously state.
//
// A refusing reading whose publisher has gone away must stop refusing. Without
// this, arming the ceiling means that any process which happens to observe the
// fleet at its ceiling — and then loses its publisher — refuses every paid
// request until it is restarted. That is a fleet-wide outage produced by a guard
// whose whole design note says it must fail open.
func TestCurrentDecaysWhenStale(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })

	// A reading that WOULD refuse, but expired a minute ago.
	Publish(State{
		OnCash:       true,
		TodayCents:   300_00,
		CeilingCents: 200_00,
		Until:        time.Now().Add(-time.Minute),
	})
	if got := Current(); got.Refuse() {
		t.Fatalf("a stale reading still refuses: %+v", got)
	}
	if got := Current(); got != (State{}) {
		t.Fatalf("a stale reading is not the zero State: %+v", got)
	}
}

// TestCurrentHonorsAFreshReading is the other half: decay must not swallow a
// reading that is still good, or arming the ceiling would be a no-op.
func TestCurrentHonorsAFreshReading(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })

	Publish(State{
		OnCash:       true,
		TodayCents:   300_00,
		CeilingCents: 200_00,
		Until:        time.Now().Add(time.Hour),
	})
	if got := Current(); !got.Refuse() {
		t.Fatalf("a fresh over-ceiling reading does not refuse: %+v", got)
	}
}

// TestUnsetUntilKeepsTodaysBehaviour is the no-op proof for the publisher that
// exists now (hanzoai/cloud apps/admin/finance), which sets no Until: adding the
// field must not change what it already does.
func TestUnsetUntilKeepsTodaysBehaviour(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })

	Publish(State{OnCash: true, TodayCents: 300_00, CeilingCents: 200_00})
	if got := Current(); !got.Refuse() {
		t.Fatalf("a reading with no Until stopped refusing: %+v", got)
	}
}
