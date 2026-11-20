// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// orgHeader names the caller's org to an upstream Hanzo runs itself. Zen and Enso
// meter and partition by it, and Hanzo Engine and hanzo-router key their prefix
// caches and stored responses on it. A vendor's API is never told who the caller
// is: an org slug is a customer's name, and a reseller has no use for it.
const orgHeader = "X-Org-Id"

// firstParty reports an upstream Hanzo runs: a hanzo.ai host, a cluster or LAN
// name, or a loopback or private address. It is hanzo-inc/zen's rule for the same
// header (zen org.go), so the org reaches the same upstreams through either door.
func firstParty(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	if host == "hanzo.ai" || strings.HasSuffix(host, ".hanzo.ai") || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".svc", ".cluster.local", ".local", ".internal"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// nameOrg sets h's org for a request to base: the org when base is an upstream
// Hanzo runs, and no org header at all otherwise, whatever h carried before.
func nameOrg(h http.Header, base, org string) {
	if org != "" && firstParty(base) {
		h.Set(orgHeader, org)
		return
	}
	h.Del(orgHeader)
}
