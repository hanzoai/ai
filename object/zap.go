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

// Package object/zap.go — Native ZAP binary protocol node.
//
// Cloud-api is a first-class ZAP node. NO gateways, NO proxies, NO sidecars,
// NO HTTP translation layers. Everything is ZAP-to-ZAP:
//
//   client → cloud-api:9651 (ZAP binary)
//   cloud-api → kv:9651     (ZAP binary)
//   cloud-api → sql:9651    (ZAP binary)
//
// Service handlers are registered via RegisterCloudHandler() from the
// controllers package (avoids circular imports).

package object

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/beego/beego/logs"
	"github.com/luxfi/zap"
)

// ── Message types ───────────────────────────────────────────────────────
//
// Cloud service types (100-199):
//   100 = Cloud service request (method dispatch)
//
// Backend types (300-399, matches sidecar protocol):
//   300 = SQL query/exec
//   301 = KV get/set/cmd

const (
	// Cloud service — native binary RPC, NO HTTP.
	MsgTypeCloud uint16 = 100

	// Gateway → cloud-api (HTTP-over-ZAP from gateway proxy).
	// Request:  method(0:Text) + path(8:Text) + headers(16:Bytes) + body(24:Bytes) + query(32:Text)
	// Response: status(0:Uint32) + body(4:Bytes) + headers(12:Bytes)
	MsgTypeHTTPRequest uint16 = 200

	// Backend sidecar protocol (KV/SQL embedded servers).
	MsgTypeSQL       uint16 = 300
	MsgTypeKV        uint16 = 301
	MsgTypeDatastore uint16 = 302

	// ── Cloud service message layout ────────────────────────────────
	// Request:  method(0:Text) + auth(8:Text) + body(16:Bytes)
	// Response: status(0:Uint32) + body(4:Bytes) + error(12:Text)
	CloudReqMethod = 0
	CloudReqAuth   = 8
	CloudReqBody   = 16

	CloudRespStatus = 0
	CloudRespBody   = 4
	CloudRespError  = 12

	// ── Sidecar message layout (matches ORM driver) ─────────────────
	sidecarReqPath  = 4
	sidecarReqBody  = 12
	sidecarRespStatus = 0
	sidecarRespBody   = 4
)

// ── Package state ───────────────────────────────────────────────────────

var (
	zapNode         *zap.Node
	kvPeerID        string
	sqlPeerID       string
	datastorePeerID string
	zapMu           sync.RWMutex
	zapReady        bool
)

// ── Initialization ──────────────────────────────────────────────────────

// InitZap starts the ZAP node and connects to KV and SQL peers.
func InitZap() {
	if os.Getenv("ZAP_ENABLED") != "true" {
		logs.Info("ZAP: disabled (set ZAP_ENABLED=true)")
		return
	}

	port := 9651
	if p := os.Getenv("ZAP_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}

	nodeID := os.Getenv("ZAP_NODE_ID")
	if nodeID == "" {
		nodeID = "cloud-api"
	}

	node := zap.NewNode(zap.NodeConfig{
		NodeID:      nodeID,
		Port:        port,
		NoDiscovery: true,
		Logger:      slog.Default(),
	})

	if err := node.Start(); err != nil {
		logs.Error("ZAP: failed to start node: %v", err)
		return
	}

	logs.Info("ZAP: node started on :%d (id=%s)", port, nodeID)

	zapMu.Lock()
	zapNode = node
	zapReady = true
	zapMu.Unlock()

	// Connect to backend peers asynchronously.
	kvAddr := os.Getenv("ZAP_KV_ADDR")
	if kvAddr == "" {
		kvAddr = "hanzo-kv:9651"
	}
	go connectPeer(node, kvAddr, "kv", &kvPeerID)

	sqlAddr := os.Getenv("ZAP_SQL_ADDR")
	if sqlAddr == "" {
		sqlAddr = "sql.hanzo.svc:9651"
	}
	go connectPeer(node, sqlAddr, "sql", &sqlPeerID)

	// Datastore (ClickHouse) — optional, for observability traces.
	if datastoreAddr := os.Getenv("ZAP_DATASTORE_ADDR"); datastoreAddr != "" {
		go connectPeer(node, datastoreAddr, "datastore", &datastorePeerID)
	}
}

// GetZapNode returns the ZAP node for handler registration.
// Used by controllers package to register service handlers.
func GetZapNode() *zap.Node {
	zapMu.RLock()
	defer zapMu.RUnlock()
	return zapNode
}

// ZapEnabled returns true if the ZAP node is running.
func ZapEnabled() bool {
	zapMu.RLock()
	defer zapMu.RUnlock()
	return zapReady && zapNode != nil
}

// StopZap gracefully shuts down the ZAP node.
func StopZap() {
	zapMu.Lock()
	defer zapMu.Unlock()
	if zapNode != nil {
		zapNode.Stop()
		zapNode = nil
		zapReady = false
		logs.Info("ZAP: node stopped")
	}
}

// connectPeer retries connecting to a ZAP peer with backoff.
func connectPeer(node *zap.Node, addr, name string, peerIDOut *string) {
	peersBefore := make(map[string]bool)
	for _, p := range node.Peers() {
		peersBefore[p] = true
	}

	for attempt := 1; attempt <= 30; attempt++ {
		if err := node.ConnectDirect(addr); err != nil {
			logs.Warn("ZAP: connect %s (%s) attempt %d: %v", name, addr, attempt, err)
			time.Sleep(2 * time.Second)
			continue
		}

		for _, p := range node.Peers() {
			if !peersBefore[p] {
				zapMu.Lock()
				*peerIDOut = p
				zapMu.Unlock()
				logs.Info("ZAP: connected to %s at %s (peer=%s)", name, addr, p)
				return
			}
		}

		peers := node.Peers()
		if len(peers) > 0 {
			zapMu.Lock()
			*peerIDOut = peers[len(peers)-1]
			zapMu.Unlock()
			logs.Info("ZAP: connected to %s at %s (peer=%s)", name, addr, *peerIDOut)
			return
		}
	}
	logs.Error("ZAP: failed to connect to %s at %s after 30 attempts", name, addr)
}

// ── KV client (native ZAP-to-ZAP) ──────────────────────────────────────

// ZapKVGet fetches a key from KV via native ZAP binary.
func ZapKVGet(ctx context.Context, key string) (string, error) {
	zapMu.RLock()
	node, peer := zapNode, kvPeerID
	zapMu.RUnlock()

	if node == nil || peer == "" {
		return "", fmt.Errorf("zap: kv not connected")
	}

	body, _ := json.Marshal(map[string]string{"key": key})
	status, resp, err := zapCallBackend(ctx, node, peer, MsgTypeKV, "/get", body)
	if err != nil {
		return "", err
	}
	if status != 200 || len(resp) == 0 {
		return "", fmt.Errorf("zap: kv get %q: status %d", key, status)
	}

	var val string
	if err := json.Unmarshal(resp, &val); err != nil {
		return string(resp), nil
	}
	return val, nil
}

// ZapKVSet stores a key/value pair via native ZAP binary.
func ZapKVSet(ctx context.Context, key, value string) error {
	zapMu.RLock()
	node, peer := zapNode, kvPeerID
	zapMu.RUnlock()

	if node == nil || peer == "" {
		return fmt.Errorf("zap: kv not connected")
	}

	body, _ := json.Marshal(map[string]string{"key": key, "value": value})
	status, _, err := zapCallBackend(ctx, node, peer, MsgTypeKV, "/set", body)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("zap: kv set %q: status %d", key, status)
	}
	return nil
}

// ZapKVSetEx stores a key/value with TTL via native ZAP binary.
func ZapKVSetEx(ctx context.Context, key, value string, ttlSeconds int) error {
	zapMu.RLock()
	node, peer := zapNode, kvPeerID
	zapMu.RUnlock()

	if node == nil || peer == "" {
		return fmt.Errorf("zap: kv not connected")
	}

	body, _ := json.Marshal(map[string]interface{}{
		"cmd":  "SETEX",
		"args": []interface{}{key, ttlSeconds, value},
	})
	status, _, err := zapCallBackend(ctx, node, peer, MsgTypeKV, "/cmd", body)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("zap: kv setex %q: status %d", key, status)
	}
	return nil
}

// ZapKVDel deletes a key via native ZAP binary.
func ZapKVDel(ctx context.Context, key string) error {
	zapMu.RLock()
	node, peer := zapNode, kvPeerID
	zapMu.RUnlock()

	if node == nil || peer == "" {
		return fmt.Errorf("zap: kv not connected")
	}

	body, _ := json.Marshal(map[string]interface{}{
		"cmd":  "DEL",
		"args": []string{key},
	})
	_, _, err := zapCallBackend(ctx, node, peer, MsgTypeKV, "/cmd", body)
	return err
}

// ── SQL client (native ZAP-to-ZAP) ─────────────────────────────────────

// ZapSQLQuery executes a read query via native ZAP binary.
func ZapSQLQuery(ctx context.Context, sql string, args ...interface{}) ([]map[string]interface{}, error) {
	zapMu.RLock()
	node, peer := zapNode, sqlPeerID
	zapMu.RUnlock()

	if node == nil || peer == "" {
		return nil, fmt.Errorf("zap: sql not connected")
	}

	body, _ := json.Marshal(map[string]interface{}{"sql": sql, "args": args})
	status, resp, err := zapCallBackend(ctx, node, peer, MsgTypeSQL, "/query", body)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("zap: sql query: status %d", status)
	}

	var rows []map[string]interface{}
	if err := json.Unmarshal(resp, &rows); err != nil {
		return nil, fmt.Errorf("zap: sql unmarshal: %w", err)
	}
	return rows, nil
}

// ZapSQLExec executes a write query via native ZAP binary.
func ZapSQLExec(ctx context.Context, sql string, args ...interface{}) error {
	zapMu.RLock()
	node, peer := zapNode, sqlPeerID
	zapMu.RUnlock()

	if node == nil || peer == "" {
		return fmt.Errorf("zap: sql not connected")
	}

	body, _ := json.Marshal(map[string]interface{}{"sql": sql, "args": args})
	status, _, err := zapCallBackend(ctx, node, peer, MsgTypeSQL, "/exec", body)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("zap: sql exec: status %d", status)
	}
	return nil
}

// ── Datastore client (native ZAP-to-ZAP → ClickHouse) ───────────────────

// ZapDatastoreExec executes an INSERT/DDL on ClickHouse via native ZAP binary.
func ZapDatastoreExec(ctx context.Context, sqlStmt string, args ...interface{}) error {
	zapMu.RLock()
	node, peer := zapNode, datastorePeerID
	zapMu.RUnlock()

	if node == nil || peer == "" {
		return fmt.Errorf("zap: datastore not connected")
	}

	body, _ := json.Marshal(map[string]interface{}{"sql": sqlStmt, "args": args})
	status, _, err := zapCallBackend(ctx, node, peer, MsgTypeDatastore, "/exec", body)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("zap: datastore exec: status %d", status)
	}
	return nil
}

// DatastoreEnabled returns true if the datastore peer is connected.
func DatastoreEnabled() bool {
	zapMu.RLock()
	defer zapMu.RUnlock()
	return zapReady && datastorePeerID != ""
}

// ── Gateway response builder ─────────────────────────────────────────────

// BuildGatewayResponse creates a response in the gateway's expected format.
// Layout: status(0:Uint32) + body(4:Bytes) + headers(12:Bytes)
func BuildGatewayResponse(status uint32, body []byte, headers []byte) (*zap.Message, error) {
	b := zap.NewBuilder(len(body) + len(headers) + 64)
	obj := b.StartObject(20)
	obj.SetUint32(0, status)
	if len(body) > 0 {
		obj.SetBytes(4, body)
	}
	if len(headers) > 0 {
		obj.SetBytes(12, headers)
	}
	obj.FinishAsRoot()
	data := b.FinishWithFlags(MsgTypeHTTPRequest << 8)
	return zap.Parse(data)
}

// ── Backend message I/O ─────────────────────────────────────────────────

// zapCallBackend builds a sidecar-format message and sends it via ZAP.
func zapCallBackend(ctx context.Context, node *zap.Node, peerID string, msgType uint16, path string, body []byte) (uint32, []byte, error) {
	b := zap.NewBuilder(len(body) + 128)
	obj := b.StartObject(20)
	obj.SetText(sidecarReqPath, path)
	obj.SetBytes(sidecarReqBody, body)
	obj.FinishAsRoot()
	data := b.FinishWithFlags(msgType << 8)

	msg, err := zap.Parse(data)
	if err != nil {
		return 0, nil, fmt.Errorf("zap: build: %w", err)
	}

	resp, err := node.Call(ctx, peerID, msg)
	if err != nil {
		return 0, nil, fmt.Errorf("zap: call: %w", err)
	}

	root := resp.Root()
	return root.Uint32(sidecarRespStatus), root.Bytes(sidecarRespBody), nil
}

// ── Cloud service response builder ──────────────────────────────────────

// BuildCloudResponse creates a native ZAP cloud service response.
// Used by controllers to build responses for incoming cloud requests.
func BuildCloudResponse(status uint32, body []byte, errMsg string) (*zap.Message, error) {
	b := zap.NewBuilder(len(body) + len(errMsg) + 64)
	obj := b.StartObject(20)
	obj.SetUint32(CloudRespStatus, status)
	if len(body) > 0 {
		obj.SetBytes(CloudRespBody, body)
	}
	if errMsg != "" {
		obj.SetText(CloudRespError, errMsg)
	}
	obj.FinishAsRoot()
	data := b.FinishWithFlags(MsgTypeCloud << 8)
	return zap.Parse(data)
}
