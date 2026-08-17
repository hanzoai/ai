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

	"github.com/hanzoai/ai/log"
	"github.com/luxfi/zap/forward"

	"github.com/hanzoai/ai/object"
)

// InitForwardBridge registers the canonical HTTP-over-ZAP terminal on the
// inference node, dispatching every Forward to h. Pass routers.App
// (the fully-wrapped native router) so all BeforeRouter filters — the
// balance gate and auth/tenant filters — run on the bridged request before
// the route dispatches.
//
// No-op when the node is absent (ZAP_ENABLED != true) or h is nil, mirroring
// InitZapHandlers; ai's :8000 HTTP behavior is unchanged either way.
// target gives a request built in this process its request line.
//
// net/http sets RequestURI on the SERVER side only: a request a caller CONSTRUCTS
// carries a URL and an empty RequestURI. The adaptor that hands a request to the
// router reads exactly that field — so an in-process request reaches the router as
// "/" and is answered 404 whatever it asked for.
//
// Every request that crosses ZAP is built that way, by the terminal below and by the
// gateway in zap_native.go, so without this the whole ZAP plane answers 404 to
// everything. The URL is what such a caller filled in, so that is what the line
// becomes.
func target(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "" && r.URL != nil {
			r = r.Clone(r.Context())
			r.RequestURI = r.URL.RequestURI()
		}
		h.ServeHTTP(w, r)
	})
}

func InitForwardBridge(h http.Handler) {
	node := object.GetZapNode()
	if node == nil || h == nil {
		return
	}
	forward.Serve(node, target(h))
	log.Info("forward_serve: ZAP HTTP terminal registered on node %s (msg_type=%d)", node.NodeID(), forward.MsgTypeForward)
}
