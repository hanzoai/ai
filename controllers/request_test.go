package controllers

import "testing"

// TestPageAskedReadsParamP locks the page parameter name to "p". The paginator
// used to read it itself; now the caller does, so the name is asserted here —
// a "page" parameter is ignored and the caller stays on the first page.
//
// It is a table over the raw query value because that is what varies: absent,
// non-numeric and negative all have to land on something the paginator clamps
// to page 1, and none of them may be an error.
func TestPageAskedReadsParamP(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"p=3", 3},
		{"p=1", 1},
		{"page=3", 0},      // the other spelling names nothing
		{"pageSize=10", 0}, //
		{"", 0},            // absent
		{"p=abc", 0},       // non-numeric is page 1, never an error
		{"p=-3", -3},       // the paginator clamps, this does not
		{"p=3&page=9", 3},  // "p" wins
	} {
		got := pageAsked(tc.query)
		if got != tc.want {
			t.Errorf("query %q: page = %d, want %d", tc.query, got, tc.want)
		}
	}
}
