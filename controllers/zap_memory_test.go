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

package controllers

import (
	"context"
	"testing"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

// zapMemoryHandlers is the set migrated from controllers/memory.go — the same
// seven routes the controller router serves, keyed by their native cloud method.
func zapMemoryHandlers() map[string]func(context.Context, string, []byte) (*zap.Message, error) {
	return map[string]func(context.Context, string, []byte) (*zap.Message, error){
		"memory.remember": zapMemoryRememberHandler,
		"memory.search":   zapMemorySearchHandler,
		"memory.list":     zapMemoryListHandler,
		"memory.recall":   zapMemoryRecallHandler,
		"memory.facts":    zapMemoryFactsHandler,
		"memory.update":   zapMemoryUpdateHandler,
		"memory.delete":   zapMemoryDeleteHandler,
	}
}

// zapMemoryStatus decodes the status code off a native cloud response message.
func zapMemoryStatus(t *testing.T, msg *zap.Message, err error) uint32 {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	if msg == nil {
		t.Fatal("handler returned nil response message")
	}
	return msg.Root().Uint32(object.CloudRespStatus)
}

// TestZapMemoryAuthRejection is the core parity assertion: the ONE auth seam
// rejects an unauthenticated caller BEFORE any body decode or storage call, on
// every one of the seven handlers. Identity is never taken from the body, so a
// well-formed body with an empty (or unrecognized) token is still 401 — mirroring
// requireMemoryIdentity's "Please sign in first" on the router path. This path
// touches no database: zapResolveUser rejects the token by shape alone.
func TestZapMemoryAuthRejection(t *testing.T) {
	// A body that both (a) is valid for the write routes and (b) tries to smuggle
	// identity — it must never be honored because auth fails first.
	body := []byte(`{"content":"hello","q":"hello","id":"victim-org/victim","owner":"attacker","userId":"evil"}`)

	for _, auth := range []string{"", "Bearer ", "Bearer not-a-real-token"} {
		for name, h := range zapMemoryHandlers() {
			msg, err := h(context.Background(), auth, body)
			status := zapMemoryStatus(t, msg, err)
			if status != 401 {
				t.Errorf("method %q auth=%q: status=%d, want 401", name, auth, status)
			}
		}
	}
}

// TestZapMemoryReadParamDefaults proves the read-endpoint parameter parsing
// mirrors the the router memoryLimit/query semantics: q is trimmed, and an empty or
// non-positive limit falls back to the per-endpoint default (20/100) while a
// valid positive limit is honored.
func TestZapMemoryReadParamDefaults(t *testing.T) {
	p := decodeReadParams([]byte(`{"q":"  hi  ","kind":"fact","limit":5}`))
	if p.Q != "hi" {
		t.Fatalf("q not trimmed: %q", p.Q)
	}
	if p.Kind != "fact" {
		t.Fatalf("kind lost: %q", p.Kind)
	}
	if p.Limit != 5 {
		t.Fatalf("limit lost: %d", p.Limit)
	}

	if got := memoryLimitOr(0, 20); got != 20 {
		t.Fatalf("empty limit must default: got %d", got)
	}
	if got := memoryLimitOr(-3, 100); got != 100 {
		t.Fatalf("non-positive limit must default: got %d", got)
	}
	if got := memoryLimitOr(7, 20); got != 7 {
		t.Fatalf("valid limit must be honored: got %d", got)
	}
}

// TestZapMemoryIdentityNeverFromBody proves the end-to-end identity chokepoint of
// the migrated handlers: a memoryRequest decoded from a body that tries to set
// owner/userId carries no such fields, and applyMemoryIdentity force-scopes the
// stored memory to the authenticated (org, userID) — exactly like the router path.
func TestZapMemoryIdentityNeverFromBody(t *testing.T) {
	req, err := decodeMemoryRequest([]byte(`{"content":"c","kind":"note","owner":"attacker","userId":"evil"}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Content != "c" || req.Kind != "note" {
		t.Fatalf("legitimate fields lost: %+v", req)
	}

	m := &object.Memory{Content: req.Content, Kind: req.Kind, Metadata: req.Metadata}
	// The struct starts with attacker-controlled identity; the chokepoint overwrites it.
	m.Owner = "attacker-org"
	m.UserId = "attacker"
	applyMemoryIdentity(m, "real-org", "real-user")
	if m.Owner != "real-org" || m.UserId != "real-user" {
		t.Fatalf("identity not enforced: owner=%q user=%q", m.Owner, m.UserId)
	}
}
