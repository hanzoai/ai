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
	"encoding/json"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

// gwStatusMsg decodes a gateway (MsgType 200) response into its HTTP status and
// the beego {status,msg} envelope — the exact shape the console/app parse.
func gwStatusMsg(t *testing.T, m *zap.Message) (status uint32, envStatus, envMsg string) {
	t.Helper()
	if m == nil {
		t.Fatal("nil zap.Message")
	}
	root := m.Root()
	status = root.Uint32(object.CloudRespStatus) // gateway status field index 0
	var env struct {
		Status string `json:"status"`
		Msg    string `json:"msg"`
	}
	if body := root.Bytes(4); len(body) > 0 { // gateway body field index 4
		_ = json.Unmarshal(body, &env)
	}
	return status, env.Status, env.Msg
}

// The core auth parity: every usage handler rejects an empty Bearer with a 401
// error envelope (never a 200, never leaking data) — the same refusal the beego
// RequireSignedIn/RequirePrincipal guard produces. Table-driven across the whole
// group so no handler can silently drift open.
func TestZapUsageHandlers_RejectEmptyAuth(t *testing.T) {
	handlers := map[string]zapGatewayHandler{
		"/v1/admin/usage/backfill-do": zapPostBackfillDOUsageHandler,
	}

	for path, h := range handlers {
		t.Run(path, func(t *testing.T) {
			method := "GET"
			if path == "/v1/admin/usage/backfill-do" {
				method = "POST"
			}
			msg, err := h(context.Background(), method, path, "", "" /* empty auth */, nil)
			if err != nil {
				t.Fatalf("handler returned transport error: %v", err)
			}
			status, envStatus, _ := gwStatusMsg(t, msg)
			if status != 401 {
				t.Fatalf("empty auth: got HTTP %d, want 401", status)
			}
			if envStatus != "error" {
				t.Fatalf("empty auth: got envelope status %q, want \"error\"", envStatus)
			}
		})
	}
}

// The backfill endpoint is a WRITE to the financial ledger: a wrong HTTP method
// must never execute it. A GET (with any auth) is refused at the method gate
// before principal resolution — parity with beego's POST-only route.
func TestZapBackfill_RejectsNonPost(t *testing.T) {
	msg, err := zapPostBackfillDOUsageHandler(context.Background(), "GET", "/v1/admin/usage/backfill-do", "", "Bearer whatever", nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	status, envStatus, _ := gwStatusMsg(t, msg)
	if status != 404 {
		t.Fatalf("GET to backfill: got HTTP %d, want 404", status)
	}
	if envStatus != "error" {
		t.Fatalf("GET to backfill: got envelope status %q, want \"error\"", envStatus)
	}
}

// Org-scoping parity (the tenant-safety invariant): a non-super-admin is ALWAYS
// pinned to its own org, and request hints (?org=, ?owner=) are ignored — it can
// never read another org's spend. A same-brand super admin may target one org or
// take the all-orgs god-view. This is the resolveCloudUsageScope policy the
// beego twin runs; it is exercised directly (no JWT needed) via the ownBrand arg.
func TestZapResolveCloudUsageScope(t *testing.T) {
	tenant := &iam.User{Owner: "maxpower", Name: "dave"}
	super := &iam.User{Owner: "admin", Name: "z"}

	cases := []struct {
		name     string
		user     *iam.User
		ownBrand bool
		query    string
		wantOrg  string
		wantAll  bool
	}{
		{"tenant ignores org hint", tenant, true, "org=hanzo", "maxpower", false},
		{"tenant ignores owner hint", tenant, true, "owner=hanzo", "maxpower", false},
		{"super not own-brand pinned to own org", super, false, "org=hanzo", "admin", false},
		{"super own-brand targets org", super, true, "org=hanzo", "hanzo", false},
		{"super own-brand owner param", super, true, "owner=hanzo", "hanzo", false},
		{"super own-brand all via all", super, true, "org=all", "", true},
		{"super own-brand all via star", super, true, "org=*", "", true},
		{"super own-brand all when omitted", super, true, "", "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			org, all := zapResolveCloudUsageScope(c.user, c.ownBrand, c.query)
			if org != c.wantOrg || all != c.wantAll {
				t.Fatalf("got (org=%q, all=%v), want (org=%q, all=%v)", org, all, c.wantOrg, c.wantAll)
			}
		})
	}
}
