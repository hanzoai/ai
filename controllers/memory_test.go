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

package controllers

import (
	"encoding/json"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestResolveMemoryUserID proves identity comes from the gateway header first,
// then the authenticated session — and is empty (caller denied) when neither is
// present. The function has no access to the request body by construction.
func TestResolveMemoryUserID(t *testing.T) {
	if got := resolveMemoryUserID("alice", "session-bob"); got != "alice" {
		t.Fatalf("header must win: got %q", got)
	}
	if got := resolveMemoryUserID("   ", "session-bob"); got != "session-bob" {
		t.Fatalf("session fallback: got %q", got)
	}
	if got := resolveMemoryUserID("", ""); got != "" {
		t.Fatalf("unauthenticated must resolve empty: got %q", got)
	}
	// Production priority: gateway X-User-Id (JWT sub) > legacy X-IAM-User-Id > session.
	if got := resolveMemoryUserID("gw-sub", "iam-legacy", "session-user"); got != "gw-sub" {
		t.Fatalf("gateway X-User-Id must win: got %q", got)
	}
	if got := resolveMemoryUserID("", "iam-legacy", "session-user"); got != "iam-legacy" {
		t.Fatalf("legacy X-IAM-User-Id fallback: got %q", got)
	}
}

// TestApplyMemoryIdentityOverridesBody proves the identity chokepoint overwrites
// any owner/userId an attacker may have smuggled onto the struct.
func TestApplyMemoryIdentityOverridesBody(t *testing.T) {
	m := &object.Memory{Owner: "victim-org", UserId: "victim", Content: "x"}
	applyMemoryIdentity(m, "attacker-org", "attacker")
	if m.Owner != "attacker-org" || m.UserId != "attacker" {
		t.Fatalf("identity not enforced: owner=%q user=%q", m.Owner, m.UserId)
	}
}

// TestMemoryRequestIgnoresBodyIdentity proves the end-to-end mapping the
// controller performs: a body that tries to set owner/userId is parsed without
// effect, and the stored memory is scoped to the authenticated identity while
// legitimate fields survive.
func TestMemoryRequestIgnoresBodyIdentity(t *testing.T) {
	body := []byte(`{"owner":"victim-org","userId":"victim","content":"hi","kind":"fact"}`)
	var req memoryRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	m := &object.Memory{Content: req.Content, Kind: req.Kind, Metadata: req.Metadata}
	applyMemoryIdentity(m, "real-org", "real-user")

	if m.Owner != "real-org" || m.UserId != "real-user" {
		t.Fatalf("body identity leaked through: owner=%q user=%q", m.Owner, m.UserId)
	}
	if m.Content != "hi" || m.Kind != "fact" {
		t.Fatalf("legitimate fields lost: content=%q kind=%q", m.Content, m.Kind)
	}
}

// TestMemoryRequestTarget covers id/name addressing precedence.
func TestMemoryRequestTarget(t *testing.T) {
	if got := (&memoryRequest{Id: "hanzo/memory_x"}).target(); got != "hanzo/memory_x" {
		t.Fatalf("id should win: %q", got)
	}
	if got := (&memoryRequest{Name: "memory_y"}).target(); got != "memory_y" {
		t.Fatalf("name fallback: %q", got)
	}
	if got := (&memoryRequest{}).target(); got != "" {
		t.Fatalf("empty when neither set: %q", got)
	}
}

// TestMemoryLimit covers the limit parser defaults.
func TestMemoryLimit(t *testing.T) {
	cases := []struct {
		raw  string
		def  int
		want int
	}{
		{"", 20, 20},
		{"0", 20, 20},
		{"-5", 20, 20},
		{"7", 20, 7},
		{"abc", 20, 20},
	}
	for _, tc := range cases {
		if got := memoryLimit(tc.raw, tc.def); got != tc.want {
			t.Errorf("memoryLimit(%q,%d) = %d, want %d", tc.raw, tc.def, got, tc.want)
		}
	}
}
