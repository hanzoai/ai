// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

// These three build a series whose length is the number the caller asked for, so
// a number below one is refused rather than handed to make().
func TestAWindowShorterThanADayIsRefused(t *testing.T) {
	for _, n := range []int{0, -1, -1 << 40} {
		if _, err := GetActivities(n, "u", []string{"user"}, "en"); err == nil {
			t.Errorf("GetActivities(%d) answered no error", n)
		}
		if _, err := GetUsages(n, "u", "s"); err == nil {
			t.Errorf("GetUsages(%d) answered no error", n)
		}
		if _, err := GetRangeUsages("Day", n, "u", "s", "en"); err == nil {
			t.Errorf("GetRangeUsages(Day, %d) answered no error", n)
		}
	}
}
