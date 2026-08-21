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
	"net/http"
	"testing"

	"github.com/hanzoai/ai/object"
)

// memoryController is a request that NAMES a victim in the two headers a gateway
// would mint, and presents whatever credential the caller has.
func memoryController(auth, org, user string) *ApiController {
	c := presenting(visit(http.MethodPost, "/v1/ai/memory/remember"), auth)
	c.Fiber().Request().SetBody([]byte(`{"content":"hi"}`))
	if org != "" {
		c.Fiber().Request().Header.Set("X-Org-Id", org)
	}
	if user != "" {
		c.Fiber().Request().Header.Set("X-User-Id", user)
	}
	return c
}

// TestMemoryIdentityIsTheCredentialNotTheHeader is the security assertion for a
// per-person store: WHOSE memories a request touches is decided by the credential
// it proved, never by what it wrote in a header.
//
// The headers below are exactly what a gateway mints — which is why they used to
// be read. A gateway is not the only way to reach this controller, and on any
// other route they are two strings the caller chose: read them and an
// uncredentialed stranger addresses any user's memories by name, to read, to
// overwrite, or to delete.
func TestMemoryIdentityIsTheCredentialNotTheHeader(t *testing.T) {
	c := memoryController("", "victim-org", "victim")

	if got := c.memoryUserID(); got != "" {
		t.Fatalf("X-User-Id reached the identity: got %q, want empty", got)
	}
	if org, user, ok := c.requireMemoryIdentity(); ok {
		t.Fatalf("a caller with no credential was admitted as %q/%q", org, user)
	}

	// Nor does an unverifiable credential become one by being present.
	c = memoryController("Bearer not-a-real-credential", "victim-org", "victim")
	if got := c.memoryUserID(); got != "" {
		t.Fatalf("an unverifiable token yielded a user: %q", got)
	}
	if _, _, ok := c.requireMemoryIdentity(); ok {
		t.Fatal("an unverifiable token was admitted")
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
