// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"testing"
	"time"
)

// The two usage reports read the same messages, so they have to agree about what
// day it is. Truncating to a day works on absolute time, so the instant was
// always the same — but a time renders in its own location, and one of them was
// built from local time. On any deployment west of UTC the range view named
// yesterday while the daily view named today.
func TestBothUsageReportsAgreeOnToday(t *testing.T) {
	withStore(t)
	now := time.Now().UTC()
	if _, err := AddMessage(&Message{
		Owner: "admin", Name: "m-1", Organization: "acme", Store: "s1",
		User: "alice", Chat: "c", Author: "AI",
		CreatedTime: now.Format(time.RFC3339), TokenCount: 5, Price: 0.01, Currency: "USD",
	}); err != nil {
		t.Fatal(err)
	}

	daily, err := GetUsages(1, "", "All", "s1")
	if err != nil {
		t.Fatal(err)
	}
	ranged, err := GetRangeUsages("Day", 1, "", "All", "s1", "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 1 || len(ranged) != 1 {
		t.Fatalf("one day gave %d and %d buckets", len(daily), len(ranged))
	}
	if daily[0].Date != ranged[0].Date {
		t.Errorf("the same day is %q to the daily report and %q to the range report",
			daily[0].Date, ranged[0].Date)
	}
	// And both counted the message.
	if daily[0].MessageCount != 1 {
		t.Errorf("the daily report counted %d messages", daily[0].MessageCount)
	}
	if ranged[0].MessageCount != 1 {
		t.Errorf("the range report counted %d messages", ranged[0].MessageCount)
	}
}

// A range type nobody serves says so rather than answering with empty buckets.
func TestARangeWeDoNotReport(t *testing.T) {
	withStore(t)
	for _, rangeType := range []string{"", "Decade", "day", "HOUR"} {
		if _, err := GetRangeUsages(rangeType, 3, "", "All", "s1", "en"); err == nil {
			t.Errorf("%q was reported on", rangeType)
		}
	}
	for _, rangeType := range []string{"Hour", "Day", "Week", "Month"} {
		got, err := GetRangeUsages(rangeType, 3, "", "All", "s1", "en")
		if err != nil {
			t.Errorf("%s: %v", rangeType, err)
			continue
		}
		if len(got) != 3 {
			t.Errorf("%s gave %d buckets, want 3", rangeType, len(got))
		}
	}
}
