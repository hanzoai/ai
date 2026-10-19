// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"time"

	"github.com/hanzoai/outbound"
)

// Public is the client for a URL chosen by whoever wrote the request — a document
// a person named, an image in a message, a page to crawl. This process runs
// inside the cluster, where in-namespace service names resolve and
// 169.254.169.254 hands out credentials to anyone who asks, so an unrestricted
// GET on such a URL is a credential read, not a fetch.
//
// It is hanzoai/outbound's client, the one rule every Hanzo service reaches a
// tenant's address by: judged at the dial, on the address the socket connects to
// (so a name that answers public once and private the next time is refused on the
// answer that would have been used), every redirect a new dial judged the same
// way, no proxy, and the IANA special-purpose registries rather than net.IP's
// predicates. A refusal is outbound.ErrRefused.
//
// A test that must reach a loopback server swaps this variable; outbound has no
// switch that turns the rule off.
var Public = outbound.Client(outbound.Options{
	Timeout:   60 * time.Second,
	Redirects: 5,
	Limit:     64 << 20,
})
