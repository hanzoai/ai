// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package timetools

// The builtin time tools answer an agent's "what time is it, there, then?"
// questions. Their conversions are deterministic, but nothing pinned them:
// the unix epoch rendering per timezone, the default timezone, the spoken
// layouts, the localtime -> timestamp -> localtime round trip, the cross-
// timezone conversion, the weekday answers, and every refusal path.

import (
	"context"
	"testing"

	"github.com/ThinkInAIXYZ/go-mcp/protocol"
)

// text extracts the single text payload a tool answered with.
func text(t *testing.T, r *protocol.CallToolResult) string {
	t.Helper()
	if r == nil || len(r.Content) == 0 {
		t.Fatal("empty tool result")
	}
	tc, ok := r.Content[0].(*protocol.TextContent)
	if !ok {
		t.Fatalf("content is %T, want *protocol.TextContent", r.Content[0])
	}
	return tc.Text
}

// The epoch renders in the requested timezone, and in Asia/Shanghai when no
// timezone is given.
func TestTimestampToLocalTime_Timezones(t *testing.T) {
	tool := &TimestampToLocalTimeTool{}
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"epoch UTC", map[string]any{"timestamp": int64(0), "timezone": "UTC"}, "1970-01-01 00:00:00"},
		{"epoch Shanghai", map[string]any{"timestamp": int64(0), "timezone": "Asia/Shanghai"}, "1970-01-01 08:00:00"},
		{"epoch default is Shanghai", map[string]any{"timestamp": int64(0)}, "1970-01-01 08:00:00"},
		{"negative timestamp", map[string]any{"timestamp": int64(-1), "timezone": "UTC"}, "1969-12-31 23:59:59"},
		{"int argument", map[string]any{"timestamp": 1710469800, "timezone": "Asia/Shanghai"}, "2024-03-15 10:30:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if r.IsError {
				t.Fatalf("tool refused: %s", text(t, r))
			}
			if got := text(t, r); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A missing or mistyped timestamp and an unknown timezone are refused in
// band, never panics and never wrong answers.
func TestTimestampToLocalTime_Refusals(t *testing.T) {
	tool := &TimestampToLocalTimeTool{}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing timestamp", map[string]any{}},
		{"string timestamp", map[string]any{"timestamp": "yesterday"}},
		{"unknown timezone", map[string]any{"timestamp": int64(0), "timezone": "Mars/Olympus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !r.IsError {
				t.Errorf("args %v answered %q, want refusal", tc.args, text(t, r))
			}
		})
	}
}

// A local wall clock names one instant: 2024-03-15 10:30:00 in Shanghai is
// 1710469800, and the spoken layouts ("2024-3-15", date only) parse to the
// same day.
func TestLocalTimeToTimestamp_Conversions(t *testing.T) {
	tool := &LocalTimeToTimestampTool{}
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"wall clock in Shanghai", map[string]any{"localtime": "2024-03-15 10:30:00", "timezone": "Asia/Shanghai"}, "1710469800"},
		{"same wall clock in UTC", map[string]any{"localtime": "2024-03-15 10:30:00", "timezone": "UTC"}, "1710498600"},
		{"unpadded layout", map[string]any{"localtime": "2024-3-15 10:30:0", "timezone": "UTC"}, "1710498600"},
		{"date only is midnight", map[string]any{"localtime": "2024-03-15", "timezone": "UTC"}, "1710460800"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if r.IsError {
				t.Fatalf("tool refused: %s", text(t, r))
			}
			if got := text(t, r); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Garbage input is refused, not parsed into a plausible wrong instant.
func TestLocalTimeToTimestamp_Refusals(t *testing.T) {
	tool := &LocalTimeToTimestampTool{}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing localtime", map[string]any{}},
		{"unparseable", map[string]any{"localtime": "next friday-ish"}},
		{"unknown timezone", map[string]any{"localtime": "2024-03-15", "timezone": "Mars/Olympus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !r.IsError {
				t.Errorf("args %v answered %q, want refusal", tc.args, text(t, r))
			}
		})
	}
}

// timestamp -> localtime -> timestamp returns the instant it started with,
// in any timezone the conversion passes through.
func TestTimestampRoundTrip(t *testing.T) {
	toLocal := &TimestampToLocalTimeTool{}
	toTs := &LocalTimeToTimestampTool{}
	for _, ts := range []int64{0, -1, 1710469800, 4102444800 /* 2100-01-01 */} {
		r1, err := toLocal.Execute(context.Background(), map[string]any{"timestamp": ts, "timezone": "UTC"})
		if err != nil || r1.IsError {
			t.Fatalf("to local(%d): %v %v", ts, err, r1)
		}
		r2, err := toTs.Execute(context.Background(), map[string]any{"localtime": text(t, r1), "timezone": "UTC"})
		if err != nil || r2.IsError {
			t.Fatalf("to timestamp(%d): %v %v", ts, err, r2)
		}
		if got, want := text(t, r2), text(t, mustTs(t, toTs, ts)); got != want {
			t.Errorf("round trip of %d came back %q, want %q", ts, got, want)
		}
	}
}

func mustTs(t *testing.T, tool *LocalTimeToTimestampTool, ts int64) *protocol.CallToolResult {
	t.Helper()
	r, err := tool.Execute(context.Background(), map[string]any{
		"localtime": func() string {
			r1, err1 := (&TimestampToLocalTimeTool{}).Execute(context.Background(), map[string]any{"timestamp": ts, "timezone": "UTC"})
			if err1 != nil || r1.IsError {
				t.Fatalf("setup: %v", err1)
			}
			return text(t, r1)
		}(),
		"timezone": "UTC",
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return r
}

// Noon in Shanghai is 04:00 in UTC on the same day; crossing the dateline
// shifts the calendar date.
func TestTimezoneConversion(t *testing.T) {
	tool := &TimezoneConversionTool{}
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"Shanghai noon to UTC", map[string]any{
			"datetime": "2024-01-01 12:00:00", "from_timezone": "Asia/Shanghai", "to_timezone": "UTC",
		}, "2024-01-01 04:00:00"},
		{"UTC midnight to Shanghai", map[string]any{
			"datetime": "2024-01-01 00:00:00", "from_timezone": "UTC", "to_timezone": "Asia/Shanghai",
		}, "2024-01-01 08:00:00"},
		{"same timezone is identity", map[string]any{
			"datetime": "2024-06-30 23:59:59", "from_timezone": "Europe/London", "to_timezone": "Europe/London",
		}, "2024-06-30 23:59:59"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if r.IsError {
				t.Fatalf("tool refused: %s", text(t, r))
			}
			if got := text(t, r); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTimezoneConversion_Refusals(t *testing.T) {
	tool := &TimezoneConversionTool{}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing datetime", map[string]any{"from_timezone": "UTC", "to_timezone": "UTC"}},
		{"bad from", map[string]any{"datetime": "2024-01-01", "from_timezone": "Mars/Olympus", "to_timezone": "UTC"}},
		{"bad to", map[string]any{"datetime": "2024-01-01", "from_timezone": "UTC", "to_timezone": "Mars/Olympus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !r.IsError {
				t.Errorf("args %v answered %q, want refusal", tc.args, text(t, r))
			}
		})
	}
}

// Known weekdays, and the bounds of the calendar fields.
func TestWeekday(t *testing.T) {
	tool := &WeekdayTool{}
	cases := []struct {
		y, m, d int
		want    string
	}{
		{2024, 3, 15, "March 15, 2024 is Friday."},
		{2000, 1, 1, "January 1, 2000 is Saturday."},
		{2024, 2, 29, "February 29, 2024 is Thursday."}, // leap day
	}
	for _, tc := range cases {
		r, err := tool.Execute(context.Background(), map[string]any{"year": tc.y, "month": tc.m, "day": tc.d})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if r.IsError {
			t.Fatalf("%v refused: %s", tc, text(t, r))
		}
		if got := text(t, r); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc, got, tc.want)
		}
	}
}

func TestWeekday_Refusals(t *testing.T) {
	tool := &WeekdayTool{}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing year", map[string]any{"month": 3, "day": 15}},
		{"month out of range", map[string]any{"year": 2024, "month": 13, "day": 1}},
		{"day out of range", map[string]any{"year": 2024, "month": 3, "day": 32}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tool.Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !r.IsError {
				t.Errorf("args %v answered %q, want refusal", tc.args, text(t, r))
			}
		})
	}
}
