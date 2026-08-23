// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package util

import "testing"

// An id is owner/name, and every read of a row in this module starts by taking it
// apart — so what happens to a malformed one is not a curiosity.
//
// The unchecked form splits at the FIRST slash, because a name may contain
// slashes; the checked form requires exactly two pieces and refuses anything
// else. They are not interchangeable, which is why both exist and why both are
// asserted here.
func TestAnIdComesApartAtItsFirstSlash(t *testing.T) {
	for _, tc := range []struct{ id, owner, name string }{
		{"admin/thing", "admin", "thing"},
		{"admin/folder/file.txt", "admin", "folder/file.txt"},
		{"admin/", "admin", ""},
		{"/thing", "", "thing"},
		// An id carrying no slash names no name. It used to be read out of range,
		// and the id arrives on a query parameter.
		{"no-slash", "no-slash", ""},
		{"", "", ""},
	} {
		t.Run(tc.id, func(t *testing.T) {
			owner, name := GetOwnerAndNameFromIdNoCheck(tc.id)
			if owner != tc.owner || name != tc.name {
				t.Errorf("= (%q, %q), want (%q, %q)", owner, name, tc.owner, tc.name)
			}
		})
	}
}

// The checked form refuses what it cannot address rather than guessing.
func TestACheckedIdIsExactlyTwoPieces(t *testing.T) {
	owner, name, err := GetOwnerAndNameFromIdWithError("admin/thing")
	if err != nil || owner != "admin" || name != "thing" {
		t.Errorf("= (%q, %q, %v), want admin/thing", owner, name, err)
	}
	for _, bad := range []string{"no-slash", "a/b/c", "", "/"} {
		if _, _, err := GetOwnerAndNameFromIdWithError(bad); err == nil && bad != "/" {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// And the two directions agree: an id built from a pair comes back as that pair.
func TestAnIdRoundTrips(t *testing.T) {
	for _, tc := range [][2]string{{"admin", "thing"}, {"acme", "a-b-c"}, {"admin", "folder/file"}} {
		id := GetIdFromOwnerAndName(tc[0], tc[1])
		owner, name := GetOwnerAndNameFromIdNoCheck(id)
		if owner != tc[0] || name != tc[1] {
			t.Errorf("%q -> (%q, %q), want (%q, %q)", id, owner, name, tc[0], tc[1])
		}
	}

	// GetId is the same join except when the name is already an id, which is how
	// a caller passes one through without doubling its owner.
	if got := GetId("admin", "acme/thing"); got != "acme/thing" {
		t.Errorf("GetId with a name that is already an id = %q", got)
	}
	if got := GetId("admin", "thing"); got != "admin/thing" {
		t.Errorf("GetId = %q, want admin/thing", got)
	}
}

// A page size that is not a number is not a page size.
//
// This is read straight from request input at most of its call sites, and it used
// to panic — so "?pageSize=abc" was a 500 with a stack trace on every paged
// listing. 0 is the answer because 0 is what the paginator already reads as "use
// the default".
func TestANumberThatIsNotANumberIsZero(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"10", 10}, {"0", 0}, {"-5", -5},
		{"abc", 0}, {"", 0}, {"1.5", 0}, {" 10", 0}, {"10x", 0},
		{"99999999999999999999", 0}, // beyond an int, and not a crash
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := ParseInt(tc.in); got != tc.want {
				t.Errorf("ParseInt(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// And the paginator reads that zero as "use the default" rather than dividing by
// it, which is why zero is a safe answer to give it.
func TestAZeroPageSizeIsTheDefaultAndNotADivisionByZero(t *testing.T) {
	p := NewPaginator(1, ParseInt("abc"), 100)
	if p.Offset() != 0 {
		t.Errorf("offset = %d on the first page, want 0", p.Offset())
	}
	if n := p.Nums(); n != 100 {
		t.Errorf("nums = %d, want 100", n)
	}
}
