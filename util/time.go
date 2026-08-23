// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

package util

import "time"

// Every timestamp this package writes is UTC.
//
// It is stored as text and ordered as text — created_time is a TEXT column and
// the store sorts it lexicographically — so an offset that varies makes the sort
// stop meaning time. Two spellings of one instant, 2026-08-23T01:00:00Z and
// 2026-08-22T20:00:00-05:00, sort five hours apart. Nothing depends on this more
// than the usage reports, which walk a store's messages once and move a day
// counter forward, never back.
//
// A UTC container already wrote Z, so this changes nothing where it runs and
// makes the rule hold wherever else it does.
//
// The fraction is written to a fixed three places for the same reason. Go's .999
// OMITS trailing zeros, so one function produced ...11Z, ...11.001Z and ...11.1Z
// depending on the millisecond — and no fraction at all sorts AFTER every
// fraction, because Z follows the digits. A message at exactly .000 therefore
// sorted last within its second, which is how a chat could show an answer above
// the question it answered.

func GetCurrentTime() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

// GetTimeAgo renders the instant d before now in the SAME format as GetCurrentTime.
// A stored timestamp is compared against a cutoff as a STRING — that is what the
// database does with these columns — so the two have to be formatted identically or
// the comparison means nothing.
func GetTimeAgo(d time.Duration) string {
	return time.Now().UTC().Add(-d).Truncate(time.Second).Format(time.RFC3339)
}

func GetCurrentTimeWithMilli() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// GetCurrentTimeEx answers now, but never at or before timestamp — it is what
// keeps the messages of one chat in the order they were written.
//
// A timestamp it cannot read is simply not a lower bound, so the answer is now.
// Its callers pass a stored row's CreatedTime, and a row written before that
// field existed carries the empty string: panicking made every answer in such a
// chat fail on a field nobody is looking at.
func GetCurrentTimeEx(timestamp string) string {
	tm := time.Now().UTC()
	inputTime, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return tm.Format("2006-01-02T15:04:05.000Z07:00")
	}

	if !tm.After(inputTime.Add(1 * time.Millisecond)) {
		tm = inputTime.UTC().Add(1 * time.Millisecond)
	}

	return tm.Format("2006-01-02T15:04:05.000Z07:00")
}

// GetCurrentTimeBasedOnLastMilli answers now, but never at or before timestamp,
// which is how a batch of records gets distinct times in the order it was given.
// A timestamp it cannot read is not a lower bound; the answer is now.
func GetCurrentTimeBasedOnLastMilli(timestamp string) string {
	tm := time.Now().UTC()
	// The layout READ with keeps .999, which accepts a fraction of any length or
	// none — every row written before the format was fixed has one of those shapes,
	// and a layout of .000 demands exactly three digits and would reject them all.
	inputTime, err := time.Parse("2006-01-02T15:04:05.999Z07:00", timestamp)
	if err != nil {
		return tm.Format("2006-01-02T15:04:05.000Z07:00")
	}

	if !tm.After(inputTime.Add(1 * time.Millisecond)) {
		tm = inputTime.UTC().Add(1 * time.Millisecond)
	}

	return tm.Format("2006-01-02T15:04:05.000Z07:00")
}

func AdjustTimeFromSecToMilli(timeStr string, offsetMs int) string {
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return timeStr
	}

	adjustedTime := t.UTC().Add(time.Duration(offsetMs) * time.Millisecond)

	return adjustedTime.Format("2006-01-02T15:04:05.000Z07:00")
}

// GetCurrentUnixTime returns the current Unix timestamp in seconds
func GetCurrentUnixTime() int64 {
	return time.Now().Unix()
}
