// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"fmt"
	"testing"
	"time"
)

// Usage is reported as a running total per day, built by walking the messages
// once and moving a day counter forward. A message out of time order is therefore
// counted on the wrong day and on every day after it — and a store holds messages
// under more than one owner, since a spoken answer is stored under its provider's
// while a chat's is stored under the namespace every chat shares.
func TestDailyUsageCountsEachDayItsOwn(t *testing.T) {
	withStore(t)
	now := time.Now().UTC()
	day := func(back int) string {
		return now.AddDate(0, 0, -back).Truncate(24 * time.Hour).
			Add(12 * time.Hour).Format(time.RFC3339)
	}

	// Two owners, interleaved in time: two messages two days ago, one yesterday,
	// three today. Seeded newest first, so nothing but the query's own order can
	// put them right.
	seed := []struct {
		owner string
		back  int
	}{
		{"admin", 0}, {"hanzo", 0}, {"admin", 0},
		{"hanzo", 1},
		{"hanzo", 2}, {"admin", 2},
	}
	for i, s := range seed {
		if _, err := AddMessage(&Message{
			Owner: s.owner, Name: fmt.Sprintf("m-%d", i), Organization: "acme",
			Store: "s1", User: "alice", Chat: "c", Author: "AI",
			CreatedTime: day(s.back), TokenCount: 10, Price: 0.01, Currency: "USD",
		}); err != nil {
			t.Fatal(err)
		}
	}

	usages, err := GetUsages(3, "All", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 3 {
		t.Fatalf("reported %d days, want 3", len(usages))
	}

	// Running totals: 2 after the oldest day, 3 after yesterday, 6 after today.
	want := []int{2, 3, 6}
	for i, w := range want {
		if usages[i].MessageCount != w {
			t.Errorf("day %d (%s) reports %d messages, want %d running total",
				i, usages[i].Date, usages[i].MessageCount, w)
		}
	}
	// A running total never goes down.
	for i := 1; i < len(usages); i++ {
		if usages[i].MessageCount < usages[i-1].MessageCount {
			t.Errorf("day %d reports fewer messages (%d) than day %d (%d)",
				i, usages[i].MessageCount, i-1, usages[i-1].MessageCount)
		}
	}
	// And the tokens follow the messages.
	if usages[2].TokenCount != 60 {
		t.Errorf("the final day reports %d tokens, want 60", usages[2].TokenCount)
	}
}
