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

// Datastore-over-ZAP client — the ZAP-native transport for the OLAP ledger.
//
// This is the ZAP counterpart to object/datastore.go's direct hanzo-ds/go
// client. It speaks to cmd/zap-bridge (baked into the hanzoai/datastore image,
// listening on :9999) which proxies to ClickHouse-native on 127.0.0.1:9000.
//
// DORMANT until cutover. Setting ZAP_DATASTORE_ADDR only CONNECTS the peer; it
// does NOT reroute the ledger — object/datastore.go's DatastoreExec/DatastoreQuery
// remain the live transport until the prod cut repoints them here and deletes the
// direct client. This split lets us prove per-org debits over ZAP (canary) before
// the money ledger stops using the direct client.
//
// Wire contract (MUST match hanzoai/datastore-legacy cmd/zap-bridge/main.go):
//   - MsgType 302 (MsgTypeDatastore); sent as FinishWithFlags(302<<8), the bridge
//     dispatches on the low byte (302 & 0xFF = 0x2E).
//   - Request object (fixed size 36): path@4(Text), body@12(Bytes),
//     ts@20(Uint64 Unix-ms), hmac@28(Bytes).
//   - HMAC-SHA256(key, path || 0x00 || body || 0x00 || uint64-LE(ts)); key is
//     base64(DATASTORE_BRIDGE_HMAC_KEY), >=32 bytes. ±60s replay window.
//   - Response: status@0(Uint32), body@4(Bytes). /query returns NDJSON rows;
//     /exec returns {"ok":true}. Body is strict JSON {"sql","args"}.
package object

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/beego/logs"
	"github.com/luxfi/zap"
)

// MsgTypeDatastore is the sidecar MsgType for the datastore bridge. Canonical
// ID 302 (matches hanzoai/orm db zapMsgTypeDatastore and cmd/zap-bridge). The
// wire dispatch key is its low byte (302 & 0xFF = 0x2E), consistent with the
// SQL(300)/KV(301)/Docdb(303) peers.
const MsgTypeDatastore uint16 = 302

// msgTypeDatastoreFlags is the 16-bit flags value carried on the wire.
// FinishWithFlags packs the MsgType's low byte into the high byte and the
// bridge dispatches on flags>>8 (== MsgTypeDatastore & 0xFF = 0x2E). Precomputed
// as a constant because the literal MsgTypeDatastore<<8 (302<<8 = 77312) would
// overflow uint16 at compile time; the runtime peers shift a variable instead.
const msgTypeDatastoreFlags uint16 = (MsgTypeDatastore & 0xFF) << 8

// Datastore bridge frame fields not already shared with the sidecar peers.
// path@4/body@12/respStatus@0/respBody@4 reuse the sidecar* constants in zap.go;
// the datastore bridge adds a replay-guard timestamp and an HMAC signature.
const (
	dsFieldTS         = 20 // Uint64 Unix-ms timestamp (replay guard)
	dsFieldHMAC       = 28 // Bytes   HMAC-SHA256 signature
	dsObjectFixedSize = 36 // highest field end (28+8); passed to StartObject
)

var (
	datastorePeerID  string
	datastoreHMACKey []byte // base64-decoded DATASTORE_BRIDGE_HMAC_KEY (>=32 bytes)
)

// initDatastorePeer connects the ZAP datastore peer. Called from InitZap.
// Dormant unless ZAP_DATASTORE_ADDR is set AND a valid HMAC key is present;
// connecting the peer does not reroute the ledger (see file header).
func initDatastorePeer(node *zap.Node) {
	addr := strings.TrimSpace(os.Getenv("ZAP_DATASTORE_ADDR"))
	if addr == "" {
		return
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("DATASTORE_BRIDGE_HMAC_KEY")))
	if err != nil || len(key) < 32 {
		logs.Error("zap datastore: ZAP_DATASTORE_ADDR set but DATASTORE_BRIDGE_HMAC_KEY missing/invalid (need base64 >=32 bytes) — datastore ZAP peer disabled")
		return
	}
	zapMu.Lock()
	datastoreHMACKey = key
	zapMu.Unlock()
	go connectPeer(node, addr, "datastore", &datastorePeerID)
}

// DatastoreZapReady reports whether the ZAP datastore bridge peer is connected
// and signable. The cutover gates on this to choose the ZAP transport.
func DatastoreZapReady() bool {
	zapMu.RLock()
	defer zapMu.RUnlock()
	return zapNode != nil && datastorePeerID != "" && len(datastoreHMACKey) > 0
}

// computeDatastoreHMAC is the canonical bridge HMAC payload. It MUST byte-match
// cmd/zap-bridge computeHMAC: path || 0x00 || body || 0x00 || uint64-LE(ts).
func computeDatastoreHMAC(key []byte, path string, body []byte, ts uint64) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	h.Write([]byte{0})
	var tsbuf [8]byte
	binary.LittleEndian.PutUint64(tsbuf[:], ts)
	h.Write(tsbuf[:])
	return h.Sum(nil)
}

// zapCallDatastore builds an HMAC-signed sidecar frame and calls the bridge.
// A fresh timestamp is stamped per call so signatures stay inside the bridge's
// replay window. Symmetric to zapCallBackend, plus the ts + hmac fields.
func zapCallDatastore(ctx context.Context, path string, body []byte) (uint32, []byte, error) {
	zapMu.RLock()
	node, peer, key := zapNode, datastorePeerID, datastoreHMACKey
	zapMu.RUnlock()
	if node == nil || peer == "" {
		return 0, nil, fmt.Errorf("zap: datastore not connected")
	}
	if len(key) == 0 {
		return 0, nil, fmt.Errorf("zap: datastore HMAC key not loaded")
	}
	ts := uint64(time.Now().UnixMilli())
	mac := computeDatastoreHMAC(key, path, body, ts)

	b := zap.NewBuilder(len(body) + 128)
	obj := b.StartObject(dsObjectFixedSize)
	obj.SetText(sidecarReqPath, path)
	obj.SetBytes(sidecarReqBody, body)
	obj.SetUint64(dsFieldTS, ts)
	obj.SetBytes(dsFieldHMAC, mac)
	obj.FinishAsRoot()
	data := b.FinishWithFlags(msgTypeDatastoreFlags)
	msg, err := zap.Parse(data)
	if err != nil {
		return 0, nil, fmt.Errorf("zap: datastore build: %w", err)
	}
	resp, err := node.Call(ctx, peer, msg)
	if err != nil {
		return 0, nil, fmt.Errorf("zap: datastore call: %w", err)
	}
	root := resp.Root()
	return root.Uint32(sidecarRespStatus), root.Bytes(sidecarRespBody), nil
}

// ZapDatastoreExec runs a DDL/INSERT statement via the bridge's /exec path.
// The ZAP-native counterpart of DatastoreExec.
func ZapDatastoreExec(ctx context.Context, stmt string, args ...interface{}) error {
	body, err := json.Marshal(map[string]interface{}{"sql": stmt, "args": args})
	if err != nil {
		return fmt.Errorf("zap: datastore exec marshal: %w", err)
	}
	status, resp, err := zapCallDatastore(ctx, "/exec", body)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("zap: datastore exec: status %d: %s", status, datastoreRespErr(resp))
	}
	return nil
}

// ZapDatastoreQuery runs a SELECT via the bridge's /query path and decodes the
// NDJSON row stream into column->value maps. The ZAP-native counterpart of
// DatastoreQuery; the cu* coercers accept these decoded JSON scalars.
func ZapDatastoreQuery(ctx context.Context, query string, args ...interface{}) ([]map[string]interface{}, error) {
	body, err := json.Marshal(map[string]interface{}{"sql": query, "args": args})
	if err != nil {
		return nil, fmt.Errorf("zap: datastore query marshal: %w", err)
	}
	status, resp, err := zapCallDatastore(ctx, "/query", body)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("zap: datastore query: status %d: %s", status, datastoreRespErr(resp))
	}
	return decodeNDJSON(resp)
}

// decodeNDJSON parses the bridge's newline-delimited JSON row stream. An empty
// body (zero rows) yields an empty slice, never nil, so callers can range safely.
func decodeNDJSON(raw []byte) ([]map[string]interface{}, error) {
	out := make([]map[string]interface{}, 0, 16)
	dec := json.NewDecoder(bytes.NewReader(raw))
	for dec.More() {
		m := make(map[string]interface{})
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("zap: datastore ndjson decode: %w", err)
		}
		out = append(out, m)
	}
	return out, nil
}

// datastoreRespErr extracts the {"error":...} message from a non-200 response,
// falling back to the raw body. Keeps error surfaces honest for the caller.
func datastoreRespErr(resp []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(resp, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(resp))
}
