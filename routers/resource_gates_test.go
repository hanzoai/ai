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

import "testing"

// Moving the CRUD surface to /v1/<ns>/<resource> changed two things that the
// authz and balance gates had quietly depended on: the PATH they matched, and
// the METHOD a write arrives as. Each of those is a live outage or a live hole
// if it regresses, so both are pinned here.

// TestSuperAdminGateSurvivesTheMove: every super-admin-gated endpoint must still
// be recognised as one at its new address. A miss here is an unauthenticated
// caller reading upstream provider config — which holds upstream API keys.
func TestSuperAdminGateSurvivesTheMove(t *testing.T) {
	cases := []struct{ path, method string }{
		{"/v1/ai/providers", "GET"},
		{"/v1/ai/providers/global", "GET"},
		{"/v1/ai/providers/acme/thing", "GET"},
		{"/v1/ai/providers", "POST"},
		{"/v1/ai/providers/acme/thing", "PATCH"},
		{"/v1/ai/providers/acme/thing", "PUT"},
		{"/v1/ai/providers/acme/thing", "DELETE"},
		{"/v1/ai/providers/mcp-tools", "POST"},
	}
	for _, c := range cases {
		name, ok := normalizedControllerName(c.path, c.method)
		if !ok {
			t.Fatalf("%s %s: not recognised as a /v1 path", c.method, c.path)
		}
		if !requiresSuperAdmin(name) {
			t.Errorf("%s %s -> %q does NOT require super admin — gate bypass",
				c.method, c.path, name)
		}
	}
}

// TestDemoModeBlocksNonPostWrites is the hole the verb change would have opened.
// Every write used to be a POST, so a "method != POST → allow" check was
// equivalent to "reads are allowed". Once updates became PATCH and deletes
// became DELETE, that same check would have let a demo visitor delete any
// resource in the system.
func TestDemoModeBlocksNonPostWrites(t *testing.T) {
	blocked := []struct{ method, path string }{
		{"DELETE", "/v1/ai/providers/acme/thing"},
		{"PATCH", "/v1/ai/providers/acme/thing"},
		{"PUT", "/v1/ai/providers/acme/thing"},
		{"DELETE", "/v1/auth/applications/acme/thing"},
		{"PATCH", "/v1/auth/users/acme/thing"},
		{"DELETE", "/v1/rag/stores/acme/thing"},
		{"POST", "/v1/rag/stores"},
		{"DELETE", "/v1/compute/nodes/acme/thing"},
	}
	for _, c := range blocked {
		if isAllowedInDemoMode(c.method, c.path) {
			t.Errorf("demo mode ALLOWS %s %s — a visitor could destroy real data",
				c.method, c.path)
		}
	}

	// The conversation itself stays usable, or it is not a demo.
	allowed := []struct{ method, path string }{
		{"POST", "/v1/chat/chats"},
		{"PATCH", "/v1/chat/chats/acme/thing"},
		{"DELETE", "/v1/chat/chats/acme/thing"},
		{"POST", "/v1/chat/messages"},
		{"PATCH", "/v1/chat/messages/acme/thing"},
		{"POST", "/v1/auth/signin"},
		{"POST", "/v1/auth/signin/oauth"},
		// Reads are never restricted.
		{"GET", "/v1/ai/providers"},
		{"HEAD", "/v1/rag/stores"},
	}
	for _, c := range allowed {
		if !isAllowedInDemoMode(c.method, c.path) {
			t.Errorf("demo mode BLOCKS %s %s — the demo cannot function", c.method, c.path)
		}
	}
}

// TestUsageReadsStayBalanceExempt is the outage that already happened once: a
// $0-balance org 402s on the very panel that tells it to add credit. The
// exemption used to be a literal path list, so moving usage to /v1/ops/usages
// would have silently reintroduced it.
func TestUsageReadsStayBalanceExempt(t *testing.T) {
	for _, p := range []string{
		"/v1/ops/usages",
		"/v1/ops/usages/range",
		"/v1/ops/usages/cloud",
	} {
		if !isBalanceExempt(p, "GET") {
			t.Errorf("%s is NOT balance-exempt — a $0 org would 402 on its own usage panel", p)
		}
	}
	// The exemption is for usage metadata, not a blanket namespace pass: metered
	// work under the same prefix must still be gated.
	if isBalanceExempt("/v1/chat/completions", "POST") {
		t.Error("/v1/chat/completions must NOT be balance-exempt — it is metered inference")
	}
}
