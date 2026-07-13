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
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/beego"
	zaplib "github.com/luxfi/zap"
	"github.com/luxfi/zap/forward"
)

// healthHandler builds the same fully-wrapped surface ai bridges in
// production — a *beego.ControllerRegister (which is an http.Handler) — but
// scoped to the one always-present, auth-exempt route used here: GET
// /v1/health → ApiController.Health → 200 + {"status":"ok",...}. Using the
// real ControllerRegister + real controller method proves the bridge drives
// beego routing and controller dispatch over ZAP, without booting ai's full
// DB/adapter/filter stack (the production handle is the same type).
func healthHandler() http.Handler {
	r := beego.NewControllerRegister()
	r.Add("/v1/health", &ApiController{}, "GET:Health")
	return r
}

// TestForwardBridgeServesBeegoHandler stands up two live ZAP nodes, registers
// the canonical HTTP-over-ZAP terminal (forward.Serve) on the backend with a
// real beego handler, and asserts GET /v1/health round-trips through the
// bridge with status 200 and a JSON body. This exercises exactly what
// InitForwardBridge wires in cmd/aid/main.go.
func TestForwardBridgeServesBeegoHandler(t *testing.T) {
	gwPort := freePort(t)
	bePort := freePort(t)

	gw := zaplib.NewNode(zaplib.NodeConfig{NodeID: "gateway-test", Port: gwPort, NoDiscovery: true})
	be := zaplib.NewNode(zaplib.NodeConfig{NodeID: "ai-test", Port: bePort, NoDiscovery: true})
	if err := gw.Start(); err != nil {
		t.Fatalf("gateway node start: %v", err)
	}
	defer gw.Stop()
	if err := be.Start(); err != nil {
		t.Fatalf("backend node start: %v", err)
	}
	defer be.Stop()

	// Register the Forward terminal on the backend node — the same call
	// InitForwardBridge makes, with the same kind of beego http.Handler.
	forward.Serve(be, healthHandler())

	// Gateway dials the backend, then waits until the peer is visible.
	if err := gw.ConnectDirect("127.0.0.1:" + strconv.Itoa(bePort)); err != nil {
		t.Fatalf("gateway dial backend: %v", err)
	}
	waitForPeer(t, gw, "ai-test")

	fwd := &forward.Forwarder{Node: gw, Peer: "ai-test"}

	req, _ := http.NewRequest(http.MethodGet, "http://ai-test/v1/health", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := fwd.RoundTrip(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("RoundTrip /v1/health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	// ApiController.Health -> ResponseOk -> {"status":"ok","msg":"",...}.
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("body did not carry the health payload through the bridge: %q", body)
	}
}

// TestInitForwardBridgeNoNode asserts the wiring is a safe no-op when ZAP is
// off (object.GetZapNode() == nil) — InitForwardBridge must not panic and ai's
// HTTP behavior is unchanged. ZAP_ENABLED is unset in the test environment, so
// the node is never created.
func TestInitForwardBridgeNoNode(t *testing.T) {
	// healthHandler() is non-nil; the guard must short-circuit on the nil node.
	InitForwardBridge(healthHandler())
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForPeer(t *testing.T, n *zaplib.Node, peer string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range n.Peers() {
			if p == peer {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("peer %q not connected within deadline (peers=%v)", peer, n.Peers())
}
