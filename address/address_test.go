package address

import "testing"

func TestBucket(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"203.0.113.7", "203.0.113.7"},
		{"203.0.113.7:443", "203.0.113.7"},
		{"::ffff:203.0.113.7", "203.0.113.7"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:ffff:ffff:ffff:ffff", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2::9]:443", "2001:db8:1:2::/64"},
		{"fe80::1%en0", "fe80::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"", ""},
		{"not-an-address", "not-an-address"},
	} {
		if got := Bucket(tc.in); got != tc.want {
			t.Errorf("Bucket(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
