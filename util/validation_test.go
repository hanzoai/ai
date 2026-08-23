// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package util

import "testing"

// FilterField is the whitelist standing between a caller's text and an SQL
// IDENTIFIER position, where a bound parameter cannot protect anything.
//
// It guards two places: the LIKE column of a filtered list, and the column in
// ORDER BY. Both take their value from a request. The quoting underneath is not a
// second line of defence — dbx returns an identifier UNQUOTED when it contains
// "(", "{{" or "[[" — and SnakeString below it only lowercases and inserts
// separators, so an all-lowercase payload passes through it byte for byte.
//
// The payloads here are the ones measured reaching ORDER BY on the real builder,
// named in the comment on sortColumn. They are kept as a test so that widening
// this expression — to allow an underscore for snake_case, say, which is the
// obvious-looking edit — has to be a decision somebody makes against them.
func TestOnlyAnAlphanumericNameReachesAnIdentifierPosition(t *testing.T) {
	for _, ok := range []string{
		"createdTime", "displayName", "name", "owner", "A", "z9", "Field123",
	} {
		t.Run("allows "+ok, func(t *testing.T) {
			if !FilterField(ok) {
				t.Errorf("FilterField(%q) = false; the UI sends names of this shape", ok)
			}
		})
	}

	for _, bad := range []string{
		// measured payloads
		"(select group_concat(name) from sqlite_master)",
		"iif((select count(*) from sqlite_master)>2,name,owner)",
		"name,(select group_concat(name) from sqlite_master)",
		// the characters that make any of the above possible
		"name;drop table chat", "name--", "name/*x*/", "name'", `name"`,
		"name)", "name(", "name,owner", "name owner", "{{name}}", "[[name]]",
		// and the shapes that are simply not a column name
		"", " ", "name_time", "created_time", "naïve", "name\n", "name\tx",
	} {
		t.Run("refuses "+bad, func(t *testing.T) {
			if FilterField(bad) {
				t.Errorf("FilterField(%q) = true; this reaches an identifier position", bad)
			}
		})
	}
}
