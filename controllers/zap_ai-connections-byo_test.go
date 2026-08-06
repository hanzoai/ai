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

// Parity tests for the native ZAP AI-connections group. They drive the cores with a
// stubbed connGetProvider / sealProviderSecret (the SAME var seams the HTTP-side tests
// use) so the two hard invariants are provable without a live datastore or KMS:
//   1. identity is taken from the ZAP auth seam, never the body — empty auth is 401;
//   2. the response NEVER leaks a raw key or a kms:// reference (no-leak), and reads are
//      scoped to the resolved principal's org (org/<name>).

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

const zapConnRawKey = "sk-raw-native-zap-must-never-persist-0987654321"

var errZapSealClosed = fmt.Errorf("kms unconfigured: refusing to store plaintext")

// okMsg fails the test if a handler returned a build error, else yields the message.
func okMsgConn(t *testing.T, msg *zap.Message, err error) *zap.Message {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if msg == nil {
		t.Fatal("handler returned nil message")
	}
	return msg
}

// TestZapConnectionOrg_RejectsAnon proves identity is enforced at the auth seam: an
// empty or whitespace token is never a principal (no body can grant identity).
func TestZapConnectionOrg_RejectsAnon(t *testing.T) {
	for _, tok := range []string{"", "   ", "Bearer "} {
		if _, err := zapConnectionOrg(tok); err == nil {
			t.Errorf("zapConnectionOrg(%q) = nil err, want rejection", tok)
		}
	}
}

// TestZapConnectionsCloud_AuthRejection proves every native handler rejects an
// unauthenticated call with 401 before touching any store.
func TestZapConnectionsCloud_AuthRejection(t *testing.T) {
	ctx := context.Background()
	assert401 := func(name string, msg *zap.Message, err error) {
		got := okMsgConn(t, msg, err).Root().Uint32(object.CloudRespStatus)
		if got != 401 {
			t.Errorf("%s: unauth status = %d, want 401", name, got)
		}
	}
	listMsg, listErr := zapConnectionsListCloud(ctx, "", nil)
	assert401("list", listMsg, listErr)
	addMsg, addErr := zapConnectionsAddCloud(ctx, "", []byte(`{"provider":"openai","apiKey":"x"}`))
	assert401("add", addMsg, addErr)
	delMsg, delErr := zapConnectionsDeleteCloud(ctx, "", []byte(`{"provider":"openai"}`))
	assert401("delete", delMsg, delErr)
	useMsg, useErr := zapConnectionsUsageCloud(ctx, "", []byte(`{"provider":"openai"}`))
	assert401("usage", useMsg, useErr)
}

// TestConnListCore_HappyAndScoped proves the happy path: with no connections the list
// is all-disconnected, and the reads are org-scoped ("acme/<name>", never a
// client-supplied org).
func TestConnListCore_HappyAndScoped(t *testing.T) {
	var gotIDs []string
	restore := connGetProvider
	connGetProvider = func(id string) (*object.Provider, error) {
		gotIDs = append(gotIDs, id)
		return nil, nil
	}
	defer func() { connGetProvider = restore }()

	out := connListCore("acme")
	if out.status != 200 || out.errMsg != "" {
		t.Fatalf("connListCore status=%d err=%q, want 200/empty", out.status, out.errMsg)
	}
	views, _ := out.payload.([]aiConnResponse)
	if len(views) != len(aiConnOrder) {
		t.Fatalf("got %d views, want %d", len(views), len(aiConnOrder))
	}
	for _, v := range views {
		if v.Connected {
			t.Errorf("provider %q connected with no row", v.Provider)
		}
	}
	if len(gotIDs) == 0 || !strings.HasPrefix(gotIDs[0], "acme/") {
		t.Fatalf("reads not org-scoped: %v", gotIDs)
	}
}

// TestConnListCore_NoLeak is the NO-LEAK INVARIANT on the native path: a connected
// account reports connected:true yet the serialized cloud response carries neither the
// raw key (resolved env-first) nor the kms:// reference.
func TestConnListCore_NoLeak(t *testing.T) {
	spec, _ := aiConnSpecFor("openai")
	secretName := connectionSecretName("acme", "openai")
	ref := "kms://" + secretName
	t.Setenv(secretName, zapConnRawKey) // env-first: ProviderKeyPresent without live KMS

	restore := connGetProvider
	connGetProvider = func(id string) (*object.Provider, error) {
		if id == "acme/openai" {
			return buildConnectionProvider("acme", spec, ref), nil
		}
		return nil, nil
	}
	defer func() { connGetProvider = restore }()

	msg, err := cloudOutcome(connListCore("acme"))
	if err != nil {
		t.Fatalf("cloudOutcome: %v", err)
	}
	body := msg.Root().Bytes(object.CloudRespBody)

	var views []aiConnResponse
	if err := json.Unmarshal(body, &views); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	var openai *aiConnResponse
	for i := range views {
		if views[i].Provider == "openai" {
			openai = &views[i]
		}
	}
	if openai == nil || !openai.Connected || openai.AccountLabel != "acme/openai" {
		t.Fatalf("openai view = %+v, want connected acme/openai", openai)
	}
	js := string(body)
	if strings.Contains(js, zapConnRawKey) {
		t.Fatalf("NO-LEAK VIOLATED: native response leaked the raw key: %s", js)
	}
	if strings.Contains(js, "kms://") || strings.Contains(js, secretName) {
		t.Fatalf("NO-LEAK VIOLATED: native response leaked the kms ref: %s", js)
	}
}

// TestConnAddCore_Validation covers the reject paths: an off-allow-list provider is
// 400, and an empty key is 400 — before any seal is attempted.
func TestConnAddCore_Validation(t *testing.T) {
	if o := connAddCore("acme", "notaprovider", "sk-x"); o.status != 400 {
		t.Errorf("unknown provider status = %d, want 400", o.status)
	}
	if o := connAddCore("acme", "openai", "   "); o.status != 400 {
		t.Errorf("empty key status = %d, want 400", o.status)
	}
}

// TestConnAddCore_SealFailClosed proves fail-closed sealing: when the seal path errors
// (KMS unconfigured), connAddCore returns 503 and NEVER persists — and the error text
// does not leak the raw key.
func TestConnAddCore_SealFailClosed(t *testing.T) {
	restore := sealProviderSecret
	sealProviderSecret = func(name, value string) (string, error) {
		return "", errZapSealClosed
	}
	defer func() { sealProviderSecret = restore }()

	o := connAddCore("acme", "openai", zapConnRawKey)
	if o.status != 503 {
		t.Fatalf("seal-fail status = %d, want 503", o.status)
	}
	if strings.Contains(o.errMsg, zapConnRawKey) {
		t.Error("error text leaked the raw key")
	}
}

// TestConnDeleteCore_Idempotent proves deleting an unconnected account is a clean
// disconnected 200 (no store mutation needed).
func TestConnDeleteCore_Idempotent(t *testing.T) {
	restore := connGetProvider
	connGetProvider = func(id string) (*object.Provider, error) { return nil, nil }
	defer func() { connGetProvider = restore }()

	o := connDeleteCore("acme", "openai")
	if o.status != 200 {
		t.Fatalf("delete-unconnected status = %d, want 200", o.status)
	}
	v, _ := o.payload.(aiConnResponse)
	if v.Connected {
		t.Error("disconnected delete reported connected")
	}
}

// TestConnUsageCore_HonestEmpty proves an unconnected account degrades to a 200
// ProviderUsage with connected/available=false + a note — the UI's honest-empty
// state, never a fabricated figure or an error.
func TestConnUsageCore_HonestEmpty(t *testing.T) {
	restore := connGetProvider
	connGetProvider = func(id string) (*object.Provider, error) { return nil, nil }
	defer func() { connGetProvider = restore }()

	o := connUsageCore(context.Background(), "acme", "openai", "", "")
	if o.status != 200 || o.errMsg != "" {
		t.Fatalf("usage-unconnected = %d/%q, want 200/empty", o.status, o.errMsg)
	}
	u, _ := o.payload.(ProviderUsage)
	if u.Connected || u.Available || u.Note == "" {
		t.Errorf("usage-unconnected = %+v, want connected/available false + note", u)
	}
}

// TestZapConnections_Registered proves the group self-registered its native cloud
// methods into the shared registry that handleCloudService consults.
func TestZapConnections_Registered(t *testing.T) {
	for _, m := range []string{"ai.connections.list", "ai.connections.add", "ai.connections.delete", "ai.connections.usage"} {
		if _, ok := lookupCloudHandler(m); !ok {
			t.Errorf("registerCloud(%q) not registered", m)
		}
	}
}
