package address

import (
	"slices"
	"testing"
)

func TestBuckets(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []Bucket
	}{
		{"203.0.113.7", []Bucket{{"203.0.113.7", 1}}},
		{"203.0.113.7:443", []Bucket{{"203.0.113.7", 1}}},
		{"::ffff:203.0.113.7", []Bucket{{"203.0.113.7", 1}}},
		{"2001:db8:1:2::1", []Bucket{{"2001:db8:1:2::/64", 1}, {"2001:db8:1::/48", site}}},
		{"2001:db8:1:2:ffff:ffff:ffff:ffff", []Bucket{{"2001:db8:1:2::/64", 1}, {"2001:db8:1::/48", site}}},
		{"[2001:db8:1:2::9]:443", []Bucket{{"2001:db8:1:2::/64", 1}, {"2001:db8:1::/48", site}}},
		{"fe80::1%en0", []Bucket{{"fe80::/64", 1}, {"fe80::/48", site}}},
		{"2001:db8:1:3::1", []Bucket{{"2001:db8:1:3::/64", 1}, {"2001:db8:1::/48", site}}},
		{"", []Bucket{{"", 1}}},
		{"not-an-address", []Bucket{{"not-an-address", 1}}},
	} {
		if got := Buckets(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("Buckets(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A HURRICANE ELECTRIC /48 IS ONE SITE. Every /64 inside it is a fresh /64 bucket,
// so a count per /64 alone gives one tunnel 65,536 callers' allowance; the /48
// bucket is shared by all of them and holds the tunnel to site callers' worth.
func TestOneSiteSharesOneBucketAcrossItsSubnets(t *testing.T) {
	sites := map[string]int{}
	subnets := map[string]bool{}
	for _, a := range []string{"2001:470:1f00:1::1", "2001:470:1f00:2::1", "2001:470:1f00:ffff::1"} {
		b := Buckets(a)
		if len(b) != 2 {
			t.Fatalf("Buckets(%q) = %v: an IPv6 caller is counted by its /64 and its /48", a, b)
		}
		subnets[b[0].Key] = true
		sites[b[1].Key] = b[1].Scale
	}
	if len(subnets) != 3 {
		t.Fatalf("three /64s were %d buckets", len(subnets))
	}
	if len(sites) != 1 || sites["2001:470:1f00::/48"] != site {
		t.Fatalf("three /64s of one /48 were sites %v, want one /48 at scale %d", sites, site)
	}
	if Buckets("2001:470:1f01::1")[1].Key == "2001:470:1f00::/48" {
		t.Fatal("the neighbouring /48 shares the first one's bucket")
	}
}
