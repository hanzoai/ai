// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"errors"
	"testing"

	"github.com/hanzoai/outbound"
)

// A crawl's start URL and every link it follows are the tenant's addresses: the Go
// scraper fetches them through outbound's client, and the browser engine is handed
// only addresses outbound.Host finds public.
func TestACrawlOfThisNetworkIsRefused(t *testing.T) {
	srv, reached := inside(t)
	if _, err := ScrapePage(srv.URL + "/"); !errors.Is(err, outbound.ErrRefused) {
		t.Errorf("scrape of %s: err = %v, want outbound.ErrRefused", srv.URL, err)
	}
	if rules := fetchRobotsTxt(srv.URL); rules == nil {
		t.Fatal("robots read nothing and answered nil")
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the server on this network was reached %d times by a crawl", n)
	}
	for _, u := range []string{srv.URL + "/", "http://169.254.169.254/latest/meta-data/", "http://[fd00:ec2::254]/", "file:///etc/passwd"} {
		if err := addressable(u); !errors.Is(err, outbound.ErrRefused) {
			t.Errorf("addressable(%s) = %v, want outbound.ErrRefused", u, err)
		}
	}
}
