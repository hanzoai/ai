// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package routers

import (
	"net/http"
	"testing"
)

// HSTS is a promise about TRANSPORT, so it is made only when the transport
// deserves it — and there are two ways to learn that, which is why there are two
// positive tests. Direct TLS is one; a proxy saying so on our behalf is the other,
// and it is the one production actually uses.
const hsts = "max-age=31536000; includeSubDomains; preload"

func TestHstsSetOnTLS(t *testing.T) {
	p := ask(http.MethodGet, "/v1/health").secure()
	p = p.through(HstsFilter)
	if got := p.replied("Strict-Transport-Security"); got != hsts {
		t.Errorf("header = %q, want %q", got, hsts)
	}
}

func TestHstsSetBehindAProxy(t *testing.T) {
	p := ask(http.MethodGet, "/v1/health").with("X-Forwarded-Proto", "https")
	p = p.through(HstsFilter)
	if got := p.replied("Strict-Transport-Security"); got != hsts {
		t.Errorf("header = %q, want %q", got, hsts)
	}
}

// Every route, not one: HSTS belongs to the connection, so a path cannot earn it
// or lose it.
func TestHstsSetOnEveryRoute(t *testing.T) {
	for _, route := range []string{
		"/",
		"/v1/",
		"/v1/health",
		"/v1/models",
		"/v1/chat/completions",
	} {
		t.Run(route, func(t *testing.T) {
			p := ask(http.MethodGet, route).secure()
			p = p.through(HstsFilter)
			if got := p.replied("Strict-Transport-Security"); got != hsts {
				t.Errorf("header = %q, want %q", got, hsts)
			}
		})
	}
}

// And NOT on plain HTTP. Promising a year of HTTPS over a connection that is not
// HTTPS is how a misconfigured edge locks a domain out of itself.
func TestHstsWithheldOnPlainHTTP(t *testing.T) {
	p := ask(http.MethodGet, "/v1/health")
	p = p.through(HstsFilter)
	if got := p.replied("Strict-Transport-Security"); got != "" {
		t.Errorf("header = %q on a plain-HTTP request, want none", got)
	}
}
