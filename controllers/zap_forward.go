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

// Canonical HIP-0110 HTTP-over-ZAP terminal (luxfi/zap/forward).
//
// forward.Serve registers the MsgTypeForward (0x80) handler on the inference
// node. Each Forward envelope is decoded into an *http.Request — with the
// edge-validated identity injected as X-Org-Id / X-User-Id / X-User-IsAdmin /
// X-User-Permissions headers — served through the supplied http.Handler, and
// the buffered response returned as a ZAP Response. This is the single
// gateway→backend contract, distinct from and additive to the legacy native
// (MsgType 100) and ad-hoc gateway (MsgType 200) handlers, which stay as-is.

package controllers

import (
	"net/http"

	"github.com/beego/beego/logs"
	"github.com/luxfi/zap/forward"

	"github.com/hanzoai/ai/object"
)

// InitForwardBridge registers the canonical HTTP-over-ZAP terminal on the
// inference node, dispatching every Forward to h. Pass beego.BeeApp.Handlers
// (the fully-wrapped ControllerRegister) so all BeforeRouter filters — the
// balance gate and auth/tenant filters — run on the bridged request before
// the route dispatches.
//
// No-op when the node is absent (ZAP_ENABLED != true) or h is nil, mirroring
// InitZapHandlers; ai's :8000 HTTP behavior is unchanged either way.
func InitForwardBridge(h http.Handler) {
	node := object.GetZapNode()
	if node == nil || h == nil {
		return
	}
	forward.Serve(node, h)
	logs.Info("forward_serve: ZAP HTTP terminal registered on node %s (msg_type=%d)", node.NodeID(), forward.MsgTypeForward)
}
