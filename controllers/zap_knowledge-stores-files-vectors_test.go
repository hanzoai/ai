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
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// gatedKnowledgeMethods are this group's native methods that require an
// authenticated principal. Each MUST reject an anonymous request with a 401/403
// BEFORE touching the DB — the native-path re-enforcement of the the controller layer
// RequireSignedIn / RequireAdmin gates. No DB adapter is initialized in this
// unit test, so a handler that reached the store would panic; reaching the auth
// assertion proves the gate ran first. The public read methods (get-global-*,
// get-one, get-active) are intentionally excluded — they serve without a gate.
var gatedKnowledgeMethods = []string{
	"stores.get", "stores.get-names", "stores.update", "stores.add",
	"stores.delete", "stores.refresh-vectors", "storage.get-providers",
	"files.get", "files.update", "files.add", "files.delete",
	"files.refresh-vectors", "files.upload", "files.activate",
	"treefiles.update", "treefiles.add", "treefiles.delete",
	"vectors.get", "vectors.update", "vectors.add", "vectors.delete",
	"vectors.delete-all",
}

// TestZapKnowledgeAuthRejection asserts every gated handler fails closed on an
// empty Bearer credential, short-circuiting before any DB access.
func TestZapKnowledgeAuthRejection(t *testing.T) {
	for _, method := range gatedKnowledgeMethods {
		h, ok := zapKnowledgeCloud[method]
		if !ok {
			t.Fatalf("method %q not registered", method)
		}
		msg, err := h(context.Background(), "", []byte("{}"))
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", method, err)
		}
		got := msg.Root().Uint32(object.CloudRespStatus)
		if got != 401 && got != 403 {
			t.Errorf("%s: empty auth status = %d, want 401 or 403", method, got)
		}
	}
}

// TestZapKnowledgeBogusTokenRejected asserts an unverifiable Bearer token
// resolves to no principal (fail-secure) and is denied — never granted.
func TestZapKnowledgeBogusTokenRejected(t *testing.T) {
	msg, _ := zapGetStoresHandler(context.Background(), "Bearer not-a-real-token", []byte("{}"))
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 401 {
		t.Errorf("bogus token status = %d, want 401", got)
	}
}

// TestZapKnowledgeRegistry asserts the group's cloud + gateway tables carry the
// full route set with non-nil handlers.
func TestZapKnowledgeRegistry(t *testing.T) {
	wantCloud := append([]string{
		"stores.get-global", "stores.get-one", "files.get-global", "files.get-one",
		"files.get-active", "vectors.get-global", "vectors.get-one",
	}, gatedKnowledgeMethods...)
	for _, method := range wantCloud {
		if h, ok := zapKnowledgeCloud[method]; !ok || h == nil {
			t.Errorf("cloud method %q not registered", method)
		}
	}

	// The gateway table is deliberately EMPTY. Its entries were a second router
	// keyed on path alone, which cannot express a RESTful surface (one path,
	// several verbs, and id segments to capture). Those paths are served by the
	// native router via the MsgType 200 fallback — see zap_gateway_fallback.go.
	if len(zapKnowledgeGateway) != 0 {
		t.Errorf("gateway table has %d entries, want 0 — a second router is back",
			len(zapKnowledgeGateway))
	}
}

// TestZapKnowledgeOkEnvelopeParity asserts the success encoding matches the controller
// ResponseOk envelope ({status:"ok",data}) and the two-value form sets data2 —
// the console frontend contract the native path must preserve byte-for-byte.
func TestZapKnowledgeOkEnvelopeParity(t *testing.T) {
	data := []string{"store-a", "store-b"}
	msg, err := zapOk(data)
	if err != nil {
		t.Fatalf("zapKSFVOk: %v", err)
	}
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 200 {
		t.Fatalf("ok status = %d, want 200", got)
	}
	want, _ := json.Marshal(Response{Status: "ok", Data: data})
	if got := msg.Root().Bytes(object.CloudRespBody); string(got) != string(want) {
		t.Errorf("ok body = %s, want %s", got, want)
	}

	msg2, _ := zapOk(data, int64(2))
	want2, _ := json.Marshal(Response{Status: "ok", Data: data, Data2: int64(2)})
	if got := msg2.Root().Bytes(object.CloudRespBody); string(got) != string(want2) {
		t.Errorf("paginated body = %s, want %s", got, want2)
	}
}

// TestZapGetActiveFileHappyPath drives a real object-free happy path: the file
// activation cache lookup returns the mapped path in a 200 ok envelope, proving
// the handler's decode + encode parity without a DB.
func TestZapGetActiveFileHappyPath(t *testing.T) {
	cacheMap["ECG"] = "/cache/session-1"
	defer delete(cacheMap, "ECG")

	msg, err := zapGetActiveFileHandler(context.Background(), "", []byte(`{"prefix":"ECG"}`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := msg.Root().Uint32(object.CloudRespStatus); got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
	body := msg.Root().Bytes(object.CloudRespBody)
	var resp Response
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.Status != "ok" || resp.Data != "/cache/session-1" {
		t.Errorf("body = %s, want ok/\"/cache/session-1\"", body)
	}

	// Unknown prefix → empty string, still a 200 ok (mirrors GetActiveFile).
	miss, _ := zapGetActiveFileHandler(context.Background(), "", []byte(`{"prefix":"nope"}`))
	mb := miss.Root().Bytes(object.CloudRespBody)
	if !strings.Contains(string(mb), `"data":""`) {
		t.Errorf("miss body = %s, want empty data", mb)
	}
}
