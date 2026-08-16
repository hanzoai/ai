package routers

import (
	"testing"
	"time"
)

func free(string) Tier { return TierZenFree }

// at is a fixed instant inside a known window of every period: Wednesday
// 2026-08-12 10:00 UTC sits in the 08:00 8h window, the week that began Monday
// the 10th, and the month that began the 1st.
var at = time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

func TestPeriodsAreAlignedToTheClock(t *testing.T) {
	for _, c := range []struct {
		period     int
		start, end time.Time
	}{
		{0, time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC), time.Date(2026, 8, 12, 16, 0, 0, 0, time.UTC)},
		{1, time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)},
		{2, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	} {
		start, end := periods[c.period].window(at)
		if !start.Equal(c.start) || !end.Equal(c.end) {
			t.Errorf("%s window of %v = [%v, %v), want [%v, %v)",
				periods[c.period].name, at, start, end, c.start, c.end)
		}
	}
}

func TestTheCeilingRefusesAndNamesThePeriod(t *testing.T) {
	q := NewQuota(free, time.Hour)
	defer q.Stop()

	limit := tierQuotas[TierZenFree][0]
	for i := 0; i < limit; i++ {
		if ok, _, _ := q.Spend("k", at); !ok {
			t.Fatalf("refused at %d, below the ceiling of %d", i, limit)
		}
	}
	ok, refused, until := q.Spend("k", at)
	if ok {
		t.Fatal("admitted past the ceiling")
	}
	if refused != "8h" {
		t.Errorf("refused by %q, want the shortest period 8h", refused)
	}
	if want := time.Date(2026, 8, 12, 16, 0, 0, 0, time.UTC); !until.Equal(want) {
		t.Errorf("rolls at %v, want %v", until, want)
	}
}

// The reset is the whole point: a quota does not trickle back, it returns to zero
// when the window rolls.
func TestTheCountResetsWhenThePeriodRolls(t *testing.T) {
	q := NewQuota(free, time.Hour)
	defer q.Stop()

	limit := tierQuotas[TierZenFree][0]
	for i := 0; i < limit; i++ {
		q.Spend("k", at)
	}
	if ok, _, _ := q.Spend("k", at); ok {
		t.Fatal("admitted past the ceiling")
	}

	// One second before the roll it is still refused; one second after, admitted.
	justBefore := time.Date(2026, 8, 12, 15, 59, 59, 0, time.UTC)
	if ok, _, _ := q.Spend("k", justBefore); ok {
		t.Error("admitted before the window rolled")
	}
	justAfter := time.Date(2026, 8, 12, 16, 0, 0, 0, time.UTC)
	if ok, _, _ := q.Spend("k", justAfter); !ok {
		t.Error("still refused after the window rolled — the count did not reset")
	}
}

// A refused request must be charged to nothing, or a caller who is out for eight
// hours would go on spending their week and month while getting no service.
func TestARefusalIsNotCharged(t *testing.T) {
	q := NewQuota(free, time.Hour)
	defer q.Stop()

	limit := tierQuotas[TierZenFree][0]
	for i := 0; i < limit; i++ {
		q.Spend("k", at)
	}
	for i := 0; i < 50; i++ {
		q.Spend("k", at) // all refused
	}

	q.mu.Lock()
	week := q.keys["k"].n[1]
	q.mu.Unlock()
	if week != limit {
		t.Errorf("week count is %d after %d refusals, want %d — refusals were charged", week, 50, limit)
	}
}

func TestTheLongerPeriodRefusesOnceTheShortOnesHaveRolled(t *testing.T) {
	q := NewQuota(free, time.Hour)
	defer q.Stop()

	perEight, perWeek := tierQuotas[TierZenFree][0], tierQuotas[TierZenFree][1]
	now := at
	for spent := 0; spent < perWeek; spent += perEight {
		for i := 0; i < perEight; i++ {
			if ok, _, _ := q.Spend("k", now); !ok {
				t.Fatalf("refused at %d of the week's %d", spent+i, perWeek)
			}
		}
		now = now.Add(8 * time.Hour) // roll into the next 8h window, same week
	}
	ok, refused, _ := q.Spend("k", now)
	if ok || refused != "week" {
		t.Errorf("Spend = (%v, %q), want refused by \"week\" at the week's ceiling", ok, refused)
	}
}

func TestAnUncappedTierIsNeverRefused(t *testing.T) {
	q := NewQuota(func(string) Tier { return TierZenEnterprise }, time.Hour)
	defer q.Stop()

	for i := 0; i < tierQuotas[TierZenFree][0]*3; i++ {
		if ok, _, _ := q.Spend("k", at); !ok {
			t.Fatalf("refused an uncapped tier at %d", i)
		}
	}
}

func TestKeysAreCountedApart(t *testing.T) {
	q := NewQuota(free, time.Hour)
	defer q.Stop()

	limit := tierQuotas[TierZenFree][0]
	for i := 0; i < limit; i++ {
		q.Spend("a", at)
	}
	if ok, _, _ := q.Spend("a", at); ok {
		t.Fatal("admitted past the ceiling")
	}
	if ok, _, _ := q.Spend("b", at); !ok {
		t.Error("one key's ceiling refused another key")
	}
}
