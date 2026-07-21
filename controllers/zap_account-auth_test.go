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

	"github.com/hanzoai/ai/object"
	iam "github.com/hanzoai/iam-v1"
	"github.com/luxfi/zap"
)

// decodeCloud reads a native-cloud (MsgType 100) response back into its status,
// the decoded {status,msg,data,data2} envelope, and the ZAP-level error text.
func decodeCloud(t *testing.T, msg *zap.Message) (uint32, Response) {
	t.Helper()
	if msg == nil {
		t.Fatal("nil response message")
	}
	root := msg.Root()
	status := root.Uint32(object.CloudRespStatus)
	var env Response
	if body := root.Bytes(object.CloudRespBody); len(body) > 0 {
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("decode envelope: %v (body=%s)", err, string(body))
		}
	}
	return status, env
}

// zapAccountPrincipal is the ONE identity seam; an empty or malformed bearer must
// never resolve to a principal (fail-secure), so every authed account route
// rejects it rather than trusting the body.
func TestZapAccountPrincipal_RejectsEmptyAndUnknown(t *testing.T) {
	if _, err := zapAccountPrincipal(""); err == nil {
		t.Fatal("empty auth resolved a principal; want error")
	}
	if _, err := zapAccountPrincipal("Bearer "); err == nil {
		t.Fatal("blank bearer resolved a principal; want error")
	}
	// A token that is neither a JWT nor an IAM key is unsupported, never trusted.
	if _, err := zapAccountPrincipal("Bearer not-a-real-token"); err == nil {
		t.Fatal("garbage token resolved a principal; want error")
	}
}

// get-account on a protected wire with no credential fails LOUD (401), never a
// synthesized anonymous guest — the security invariant the beego self-heal path
// preserves.
func TestZapGetAccountHandler_NoAuthIs401(t *testing.T) {
	msg, err := zapGetAccountHandler(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	status, env := decodeCloud(t, msg)
	if status != 401 {
		t.Fatalf("status = %d; want 401", status)
	}
	if env.Status != "error" {
		t.Fatalf("envelope status = %q; want error", env.Status)
	}
}

// update-preferences is self-scoped: no credential => 401, never a body-supplied
// identity.
func TestZapUpdatePreferencesHandler_NoAuthIs401(t *testing.T) {
	msg, err := zapUpdatePreferencesHandler(context.Background(), "", []byte(`{"theme":"dark"}`))
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	status, env := decodeCloud(t, msg)
	if status != 401 {
		t.Fatalf("status = %d; want 401", status)
	}
	if env.Status != "error" {
		t.Fatalf("envelope status = %q; want error", env.Status)
	}
}

// Sign-out is idempotent and dependency-free: it always returns a 200 ok
// envelope, exactly like the hardened beego handler (no nil-deref, no 500).
func TestZapSignoutHandler_IdempotentOk(t *testing.T) {
	for _, auth := range []string{"", "Bearer whatever"} {
		msg, err := zapSignoutHandler(context.Background(), auth, nil)
		if err != nil {
			t.Fatalf("auth=%q transport error: %v", auth, err)
		}
		status, env := decodeCloud(t, msg)
		if status != 200 {
			t.Fatalf("auth=%q status = %d; want 200", auth, status)
		}
		if env.Status != "ok" {
			t.Fatalf("auth=%q envelope status = %q; want ok", auth, env.Status)
		}
	}
}

// Sign-in mints identity from {code,state}; a malformed body or a missing code is
// a 400 before any IAM exchange is attempted (no auth needed, but the body is
// still validated).
func TestZapSigninHandler_BadRequestIs400(t *testing.T) {
	// Malformed JSON.
	msg, err := zapSigninHandler(context.Background(), "", []byte("not json"))
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if status, _ := decodeCloud(t, msg); status != 400 {
		t.Fatalf("malformed body status = %d; want 400", status)
	}
	// Well-formed body, but no code.
	msg, err = zapSigninHandler(context.Background(), "", []byte(`{"state":"xyz"}`))
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if status, env := decodeCloud(t, msg); status != 400 || env.Status != "error" {
		t.Fatalf("missing-code status = %d env = %q; want 400/error", status, env.Status)
	}
}

// zapNeedsPasswordChange mirrors isSafePassword: only a legacy 270-prefixed
// 11-char id with the placeholder password is flagged.
func TestZapNeedsPasswordChange(t *testing.T) {
	cases := []struct {
		id, pwd string
		want    bool
	}{
		{"27012345678", "#NeedToModify#", true},
		{"27012345678", "already-set", false},
		{"12345678901", "#NeedToModify#", false}, // wrong prefix
		{"270", "#NeedToModify#", false},         // wrong length
		{"", "", false},
	}
	for _, c := range cases {
		u := &iam.User{Id: c.id, Password: c.pwd}
		if got := zapNeedsPasswordChange(u); got != c.want {
			t.Fatalf("id=%q pwd=%q => %v; want %v", c.id, c.pwd, got, c.want)
		}
	}
}
