// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package util

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// These timestamps are stored as text and ordered as text, so the order they
// sort in has to be the order they happened in. An offset that varies breaks
// that: 2026-08-23T01:00:00Z and 2026-08-22T20:00:00-05:00 are one instant and
// sort five hours apart.
func TestATimestampSortsTheWayItHappened(t *testing.T) {
	writers := map[string]func() string{
		"GetCurrentTime":          GetCurrentTime,
		"GetCurrentTimeWithMilli": GetCurrentTimeWithMilli,
		"GetCurrentTimeEx":        func() string { return GetCurrentTimeEx("") },
		"GetCurrentTimeBasedOnLastMilli": func() string {
			return GetCurrentTimeBasedOnLastMilli("")
		},
		"GetTimeAgo": func() string { return GetTimeAgo(0) },
	}
	for name, write := range writers {
		got := write()
		if !strings.HasSuffix(got, "Z") {
			t.Errorf("%s wrote %q — it carries an offset, so text order stops meaning time", name, got)
		}
		if _, err := time.Parse(time.RFC3339, got); err != nil {
			t.Errorf("%s wrote %q, which does not parse: %v", name, got, err)
		}
	}

	// Written in order, they sort in order.
	stamps := []string{}
	for i := 0; i < 6; i++ {
		stamps = append(stamps, GetCurrentTimeWithMilli())
		time.Sleep(2 * time.Millisecond)
	}
	sorted := append([]string(nil), stamps...)
	sort.Strings(sorted)
	for i := range stamps {
		if stamps[i] != sorted[i] {
			t.Fatalf("written in order, they sort differently:\n  written %v\n  sorted  %v", stamps, sorted)
		}
	}
}

// GetCurrentTimeEx answers now, but never at or before the timestamp it is
// given — that is what keeps one chat's messages in the order they were written.
func TestATimestampAfterTheLastOne(t *testing.T) {
	// A stamp from the future: the answer is after it in TIME, and — because these
	// are compared as text — after it as text too.
	future := time.Now().UTC().Add(time.Hour)
	given := future.Format("2006-01-02T15:04:05.000Z07:00")
	got := GetCurrentTimeEx(given)
	if got <= given {
		t.Errorf("GetCurrentTimeEx(%q) = %q, which does not sort after it", given, got)
	}
	// The same instant written with an offset gets the same treatment.
	elsewhere := time.Now().Add(time.Hour).In(time.FixedZone("east", 5*3600)).Format(time.RFC3339)
	if got := GetCurrentTimeEx(elsewhere); !strings.HasSuffix(got, "Z") {
		t.Errorf("a stamp carrying an offset produced %q", got)
	}
	// And one it cannot read is simply not a lower bound.
	if got := GetCurrentTimeEx("not a time"); !strings.HasSuffix(got, "Z") {
		t.Errorf("an unreadable stamp produced %q", got)
	}
}

// Within one second the fraction is what orders things, so every stamp in a
// second has to have one. Go's .999 omits trailing zeros, and no fraction sorts
// after every fraction — so a stamp at exactly .000 landed last in its second.
func TestEveryStampInASecondSortsWithTheRest(t *testing.T) {
	base := time.Date(2026, 8, 23, 23, 35, 11, 0, time.UTC)
	stamps := []string{}
	for _, ms := range []int{0, 1, 9, 90, 100, 500, 999} {
		stamps = append(stamps, AdjustTimeFromSecToMilli(
			base.Format(time.RFC3339), ms))
	}
	for i := 1; i < len(stamps); i++ {
		if stamps[i] <= stamps[i-1] {
			t.Errorf("%s does not sort after %s, and it happened after it", stamps[i], stamps[i-1])
		}
	}
}

// Rows written before the format was fixed have a fraction of any length, or
// none. They still have to be READ: the layout written with is exact, the layout
// read with is not, and a layout of .000 would reject every one of them.
//
// A stamp in the FUTURE is what tells the two apart. Parsed, the answer is one
// millisecond after it; unparsed, the answer is now — which is an hour earlier
// and would silently stop keeping a chat's messages in order.
func TestReadingStampsWrittenBeforeTheFormatWasFixed(t *testing.T) {
	hour := time.Now().UTC().Add(time.Hour)
	for _, layout := range []string{
		"2006-01-02T15:04:05Z07:00",        // no fraction at all
		"2006-01-02T15:04:05.0Z07:00",      // one digit
		"2006-01-02T15:04:05.000Z07:00",    // three
		"2006-01-02T15:04:05.000000Z07:00", // six
	} {
		old := hour.Format(layout)
		// The layout truncates, so compare against what was actually written.
		written, err := time.Parse(time.RFC3339, old)
		if err != nil {
			t.Fatalf("the test wrote %q, which does not parse: %v", old, err)
		}
		got := GetCurrentTimeBasedOnLastMilli(old)
		parsed, err := time.Parse(time.RFC3339, got)
		if err != nil {
			t.Errorf("%q produced %q, which does not parse: %v", old, got, err)
			continue
		}
		if !parsed.After(written) {
			t.Errorf("%q produced %q — it was not read, so the answer is now rather than after it", old, got)
		}
	}

	// And one carrying an offset is read as the instant it names.
	elsewhere := hour.In(time.FixedZone("east", 5*3600)).Format("2006-01-02T15:04:05.000Z07:00")
	written, err := time.Parse(time.RFC3339, elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	got := GetCurrentTimeBasedOnLastMilli(elsewhere)
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("%q produced %q: %v", elsewhere, got, err)
	}
	if !parsed.After(written) {
		t.Errorf("%q produced %q — an offset stopped it being read", elsewhere, got)
	}
}
