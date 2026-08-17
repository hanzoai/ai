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

package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const (
	publicTimeout = 10 * time.Second
	dialTimeout   = 5 * time.Second
	maxRedirects  = 5
)

// ErrBlocked reports a URL that resolved to an address this package refuses to
// dial. It is distinguishable from a site that was merely down, because a log
// line that cannot tell the two apart makes an attack look like an outage.
var ErrBlocked = errors.New("proxy: refused to dial a non-public address")

// Public is the client for a URL chosen by whoever wrote the request. This
// process runs inside the cluster, where in-namespace service names resolve and
// 169.254.169.254 hands out credentials to anyone who asks, so an unrestricted
// GET on a caller-supplied URL is a credential read, not a fetch.
//
// The address check is in the DIALER. A hostname checked before the request is
// resolved again by the transport, and a name that answers differently the
// second time walks through the gap between the two lookups; dialing is the one
// moment the real destination is known. Redirects re-enter the same dialer, so a
// public URL that redirects to 169.254.169.254 is refused at the hop that
// matters.
var Public = &http.Client{
	Timeout:   publicTimeout,
	Transport: guarded(),
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("proxy: stopped after %d redirects", maxRedirects)
		}
		// The dialer never sees a scheme it does not dial, so a redirect to
		// file:// or a custom scheme is refused here instead.
		if s := req.URL.Scheme; s != "http" && s != "https" {
			return fmt.Errorf("%w: redirect to scheme %q", ErrBlocked, s)
		}
		return nil
	},
}

func guarded() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !routable(ip.IP) {
				// Refuse the whole fetch rather than moving to the next address:
				// a host that answers with any internal address is not one this
				// client will reach, and trying the rest would let a target be
				// reached by listing a public address beside it.
				return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlocked, host, ip.IP)
			}
		}
		var lastErr error
		for _, ip := range ips {
			// Dial the address that was checked. Handing the hostname back to
			// the dialer would resolve it a second time.
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("proxy: no address for %s", host)
		}
		return nil, lastErr
	}
	return t
}

// routable reports whether ip is a globally routable unicast address — the only
// kind Public will dial.
//
// Written as an allowlist of "is routable" rather than a denylist of known-bad
// ranges. A denylist is one forgotten range — unique-local fc00::/7, carrier-grade
// NAT, 0.0.0.0/8, a v4-mapped v6 address carrying 127.0.0.1 past a v4-only test —
// away from being useless, and the forgotten range is noticed only when it is used.
func routable(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// Collapse a v4-mapped v6 address (::ffff:127.0.0.1) to its v4 form, so the
	// v4 rules below judge it instead of it being read as v6.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	// IsGlobalUnicast covers loopback, link-local (169.254.169.254 among them),
	// multicast and the unspecified address, for both families. IsPrivate covers
	// RFC1918 and IPv6 unique-local.
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if len(ip) == net.IPv4len {
		switch {
		case ip[0] == 0: // 0.0.0.0/8, "this network"
			return false
		case ip[0] == 100 && ip[1]&0xc0 == 64: // 100.64.0.0/10, carrier-grade NAT
			return false
		}
	}
	return true
}
