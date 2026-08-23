// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

// What actually lands in ORDER BY.
//
// The value is caller-supplied and goes into an identifier position, where a
// bound parameter cannot protect it — so the whitelist is the only thing that
// does. A refused value falls back to the default rather than erroring, because
// sort order is presentation and no caller sending a real column is affected.
//
// The default is a literal returned AFTER the check, and it contains an
// underscore, which the whitelist deliberately excludes. That is worth a test on
// its own: making the default satisfy its own guard is a natural-looking tidy-up
// that would either break the default or force the whitelist open.
func TestWhatReachesOrderBy(t *testing.T) {
	const fallback = "created_time"

	for _, tc := range []struct{ field, order, want string }{
		{"createdTime", "ascend", "created_time"},
		{"displayName", "descend", "display_name"},
		{"name", "ascend", "name"},

		// no sort asked for
		{"", "ascend", fallback},
		{"name", "", fallback},

		// the measured payloads, all falling back
		{"(select group_concat(name) from sqlite_master)", "ascend", fallback},
		{"iif((select count(*) from sqlite_master)>2,name,owner)", "ascend", fallback},
		{"name,(select group_concat(name) from sqlite_master)", "ascend", fallback},
		{"name;drop table chat", "ascend", fallback},
		{"created_time", "ascend", fallback}, // already snake: refused, and the default is returned anyway
	} {
		t.Run(tc.field+"/"+tc.order, func(t *testing.T) {
			if got := sortColumn(tc.field, tc.order); got != tc.want {
				t.Errorf("sortColumn(%q, %q) = %q, want %q", tc.field, tc.order, got, tc.want)
			}
		})
	}
}

// The direction is a binary the UI sends, and no caller text reaches SQL through
// it — anything that is not "ascend" descends.
func TestTheSortDirectionCarriesNoCallerText(t *testing.T) {
	if got := sortDirection("ascend"); got != " ASC" {
		t.Errorf("ascend = %q", got)
	}
	for _, s := range []string{"descend", "", "ASC", "; drop table chat", "ascend "} {
		if got := sortDirection(s); got != " DESC" {
			t.Errorf("sortDirection(%q) = %q, want \" DESC\"", s, got)
		}
	}
}
