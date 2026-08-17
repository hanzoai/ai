// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package routers

import (
	"net/http"

	"github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// AuthAvailableFilter refuses every request while this process cannot validate a
// bearer token, and is the ONE place that decision is made.
//
// It runs BEFORE any filter that reads a token (AutoSignin, Balance, Tenant,
// Authz), so no downstream code ever sees the difference between "this token is
// invalid" and "no cert was ever established". That difference is the whole point:
// the two look identical at a token-parsing call site, and treating the second as
// the first is what once left the service Running and answering 401 to everything,
// including free routes, with no crashloop and no alert to catch it.
//
// 503, not 401. A 401 says "your credential is wrong" to a caller whose credential
// is fine, invites a client to discard a good token and re-authenticate, and reads
// as normal traffic on every dashboard. 503 says the honest thing — the service
// cannot answer right now — and Retry-After tells a client when to come back.
//
// It is deliberately NOT a readiness probe. A probe failure would take the pod out
// of service entirely; identity being briefly unreachable should cost the requests
// that need identity, not the process. Requests are refused while it is down and
// served again on the first attempt that succeeds, with no restart.
func AuthAvailableFilter(c *zip.Ctx) error {
	if err := object.AuthReady(); err != nil {
		// Returning WITHOUT continuing is the refusal. It used to be implied by
		// having written a response; now the chain stops because this says so.
		c.SetHeader("Retry-After", "5")
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"status": "error",
			"msg":    "identity is unavailable, so this request cannot be authenticated: " + err.Error(),
		})
	}
	return c.Continue()
}
