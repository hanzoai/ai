// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// partition is the cache partition a call runs in: its org, narrowed to the
// account that pays when that is not the org itself. Members of the shared signup
// org each pay from their own wallet, so they are strangers and never share one
// (hanzo-inc/zen's Tenant.partition, the same rule).
func partition(org, payer string) string {
	if org == "" || payer == "" || payer == org {
		return org
	}
	pair, _ := json.Marshal([2]string{org, payer})
	return string(pair)
}

// cacheKeyPrefix marks a prompt_cache_key ai derived from a partition.
const cacheKeyPrefix = "hz-"

// withCacheKey sets body's prompt_cache_key to a digest of the call's partition
// and the key the caller sent, if any. A vendor routes a request to the cache that
// may hold its prefix by this key, so a caller still splits its own traffic by its
// own key, but no key a caller writes can name another partition's: the digest
// always folds the partition in. The digest also keeps the org's name from the
// vendor. A body with no partition, or that is not a JSON object, goes unchanged.
func withCacheKey(body []byte, part string) []byte {
	if part == "" {
		return body
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	var asked string
	if raw, ok := m["prompt_cache_key"]; ok && json.Unmarshal(raw, &asked) != nil {
		asked = string(raw)
	}
	pair, _ := json.Marshal([2]string{part, asked})
	sum := sha256.Sum256(pair)
	key, err := json.Marshal(cacheKeyPrefix + hex.EncodeToString(sum[:16]))
	if err != nil {
		return body
	}
	m["prompt_cache_key"] = key
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
