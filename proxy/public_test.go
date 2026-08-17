// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package proxy

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRoutableRefusesEveryInternalAddress(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1",          // loopback
		"127.9.9.9",          // the rest of 127/8
		"::1",                // loopback, v6
		"::ffff:127.0.0.1",   // loopback smuggled in a v4-mapped v6 address
		"10.1.2.3",           // RFC1918
		"172.16.5.4",         // RFC1918
		"172.31.255.255",     // RFC1918, top of the range
		"192.168.0.1",        // RFC1918
		"169.254.169.254",    // the cloud metadata endpoint
		"169.254.1.1",        // the rest of link-local
		"fe80::1",            // link-local, v6
		"fd00::1",            // unique-local, v6
		"fc00::1",            // unique-local, v6
		"0.0.0.0",            // unspecified
		"0.1.2.3",            // 0.0.0.0/8
		"::",                 // unspecified, v6
		"224.0.0.1",          // multicast
		"ff02::1",            // multicast, v6
		"100.64.0.1",         // carrier-grade NAT
		"100.127.255.255",    // carrier-grade NAT, top of the range
		"::ffff:169.254.1.1", // link-local smuggled in a v4-mapped v6 address
	} {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Fatalf("%q is not an address", addr)
		}
		if routable(ip) {
			t.Errorf("%s would be dialled", addr)
		}
	}
	if routable(nil) {
		t.Error("a missing address would be dialled")
	}
}

func TestRoutableAllowsAddressesOnTheInternet(t *testing.T) {
	for _, addr := range []string{
		"8.8.8.8",
		"1.1.1.1",
		"93.184.216.34",
		"172.32.0.1",      // just above RFC1918
		"172.15.255.255",  // just below RFC1918
		"100.63.255.255",  // just below carrier-grade NAT
		"100.128.0.0",     // just above carrier-grade NAT
		"2606:4700::1111", // v6
	} {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Fatalf("%q is not an address", addr)
		}
		if !routable(ip) {
			t.Errorf("%s would be refused", addr)
		}
	}
}

// A live server on a loopback address stands in for an in-cluster service. The
// point is that it is never reached: the check happens when the connection is
// made, so it holds for a hostname that resolves inward as well as a literal.
func TestPublicRefusesALocalServer(t *testing.T) {
	reached := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer s.Close()

	resp, err := Public.Get(s.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("the fetch succeeded")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("error is %v, want ErrBlocked", err)
	}
	if reached {
		t.Fatal("the server was reached")
	}
}

func TestPublicRefusesARedirectAwayFromHTTP(t *testing.T) {
	u, err := url.Parse("file:///etc/passwd")
	if err != nil {
		t.Fatal(err)
	}

	err = Public.CheckRedirect(&http.Request{URL: u}, nil)

	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("error is %v, want ErrBlocked", err)
	}
}

func TestPublicStopsFollowingRedirects(t *testing.T) {
	u, err := url.Parse("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{URL: u}

	if err := Public.CheckRedirect(req, make([]*http.Request, maxRedirects-1)); err != nil {
		t.Fatalf("refused hop %d: %v", maxRedirects-1, err)
	}
	if err := Public.CheckRedirect(req, make([]*http.Request, maxRedirects)); err == nil {
		t.Fatalf("followed more than %d redirects", maxRedirects)
	}
}
