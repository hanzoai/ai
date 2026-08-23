// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Fetchable answers whether a document address that arrived on a request may be
// fetched, and says why when it may not.
//
// Two things separate such an address from one this system produced for itself.
// It has to name a place rather than a path: readers that take a URL treat a
// scheme-less string as a local file, so "/etc/passwd" and "../config" are
// addresses of THIS MACHINE'S disk written in the same field. And the place has
// to be somewhere outside — a private, loopback or link-local address is this
// deployment's own neighbours, which a request can name but has no business
// reading through us.
//
// Storage URLs the system minted for itself do not come this way; this is for
// the values a person can type.
func Fetchable(raw string) error {
	address, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("that is not an address we can read: %s", raw)
	}
	if address.Scheme != "http" && address.Scheme != "https" {
		return fmt.Errorf("only http and https addresses can be read, not %q", raw)
	}
	host := address.Hostname()
	if host == "" {
		return fmt.Errorf("that address names no host: %s", raw)
	}

	// A name is resolved so a hostname pointing at a private address is refused
	// the same as the address written out. A name that resolves nowhere is left
	// to the fetch to report.
	addrs := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		addrs = append(addrs, ip)
	} else {
		resolved, err := net.LookupIP(host)
		if err != nil {
			return nil
		}
		addrs = resolved
	}
	for _, ip := range addrs {
		if withheld(ip) {
			return fmt.Errorf("%s is on this network, which is not somewhere we read from", host)
		}
	}
	return nil
}

// withheld reports whether an address belongs to the deployment rather than the
// internet: loopback, link-local (which includes the cloud metadata address),
// private ranges, and the unspecified address.
func withheld(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified()
}
