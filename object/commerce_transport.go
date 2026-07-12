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

package object

import "net/http"

// CommerceTransport is the round-tripper every commerce billing call in this
// module uses (balance read + usage debit). It is the ONE seam by which a HOST
// binary that embeds this module co-resident with commerce (hanzoai/cloud) routes
// these S2S calls to the in-process commerce handler instead of a network hop —
// the same self-routing transport the host's own metering client uses, so the ai
// router bills the SAME commerce ledger with the SAME service-token identity, with
// no socket, no DNS, and no collision with the public customer-facing billing proxy.
//
// nil (the default, e.g. standalone ai) → the client's zero-value Transport, which
// is http.DefaultTransport: a plain HTTP call to commerceEndpoint, unchanged. So a
// host sets this exactly once at boot; every other deployment is unaffected.
var CommerceTransport http.RoundTripper

// CommerceHTTPClient returns c with CommerceTransport installed when a host has
// published one, else c unchanged. The ONE place the transport is applied, so
// every commerce client (transaction, balance gate, tier cache, controller
// backstop) routes identically.
func CommerceHTTPClient(c *http.Client) *http.Client {
	if c != nil && CommerceTransport != nil {
		c.Transport = CommerceTransport
	}
	return c
}
