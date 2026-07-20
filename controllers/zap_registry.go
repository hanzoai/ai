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

// Canonical ZAP native dispatch registry — the ONE seam every migrated
// route-group self-registers into from its own init(). Populated by the
// zap_<group>.go files; consulted by handleCloudService (MsgType 100) and
// handleGatewayHTTPRequest (MsgType 200) in zap_native.go.
//
// Two protocols; the gateway carries two handler shapes:
//
//   - Native cloud (MsgType 100): method → zapHandler(ctx, auth, body).
//     Body-only RPC. registerCloud / lookupCloudHandler.
//   - Gateway HTTP-over-ZAP (MsgType 200), body-only groups: path-prefix →
//     zapHandler. The common case — the same handler serves both protocols.
//     registerGatewayPath / lookupGatewayHandler.
//   - Gateway HTTP-over-ZAP (MsgType 200), HTTP-shaped groups: path-prefix →
//     zapGatewayHandler(ctx, method, path, query, auth, body). Admin/self routes
//     that need query strings, :param paths, and GET-vs-POST.
//     registerGatewayRoute / lookupGatewayRoute.
//
// Both gateway registries resolve by LONGEST matching prefix, so a specific
// route ("/v1/admin/providers/toggle") always beats a broader sibling
// ("/v1/admin/providers"). handleGatewayHTTPRequest consults the HTTP-shaped
// registry first, then the body-only one.
//
// The strangler fallback for any route NOT registered here stays beego: the
// HTTP :8000 surface (mount.go) and the HTTP-over-ZAP forward bridge
// (zap_forward.go, MsgTypeForward) both dispatch the full beego route table
// unchanged. A lookup miss in handleGatewayHTTPRequest is not a hole — the same
// request served over the forward bridge reaches beego.

package controllers

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/luxfi/zap"
)

// zapHandler is a native-cloud (MsgType 100) or body-only gateway handler.
type zapHandler func(ctx context.Context, auth string, body []byte) (*zap.Message, error)

// zapGatewayHandler is an HTTP-shaped gateway (MsgType 200) handler that needs
// the request method, path, and query string in addition to auth + body.
type zapGatewayHandler func(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error)

type zapGatewayRoute struct {
	prefix string
	handle zapGatewayHandler
}

var (
	zapRegistryMu      sync.RWMutex
	zapCloudRegistry   = map[string]zapHandler{}
	zapGatewayRegistry = map[string]zapHandler{}
	zapGatewayRoutes   []zapGatewayRoute
)

// registerCloud maps a native-cloud (MsgType 100) method name to its handler.
func registerCloud(method string, h zapHandler) {
	zapRegistryMu.Lock()
	defer zapRegistryMu.Unlock()
	zapCloudRegistry[method] = h
}

// registerGatewayPath maps a gateway (MsgType 200) path PREFIX to a body-only
// handler. lookupGatewayHandler resolves by longest matching prefix so
// `/v1/audio/voice/…` reaches the `/v1/audio/voice` handler.
func registerGatewayPath(prefix string, h zapHandler) {
	zapRegistryMu.Lock()
	defer zapRegistryMu.Unlock()
	zapGatewayRegistry[prefix] = h
}

// registerGatewayRoute maps a gateway (MsgType 200) path PREFIX to an HTTP-shaped
// handler (method/path/query aware). For admin/self routes that need the request
// line, not just the body.
func registerGatewayRoute(prefix string, h zapGatewayHandler) {
	zapRegistryMu.Lock()
	defer zapRegistryMu.Unlock()
	zapGatewayRoutes = append(zapGatewayRoutes, zapGatewayRoute{prefix: prefix, handle: h})
}

// lookupCloudHandler returns the handler registered for a native-cloud method.
func lookupCloudHandler(method string) (zapHandler, bool) {
	zapRegistryMu.RLock()
	defer zapRegistryMu.RUnlock()
	h, ok := zapCloudRegistry[method]
	return h, ok
}

// prefixMatch reports whether prefix owns path: an exact match, or a deeper
// path segment. A prefix already ending in "/" is a segment boundary, so
// TrimSuffix+"/" normalises both forms to one rule.
func prefixMatch(prefix, path string) bool {
	return path == prefix || strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/")
}

// lookupGatewayHandler returns the body-only handler whose registered prefix is
// the longest match for path (deterministic: prefixes sorted longest-first).
func lookupGatewayHandler(path string) (zapHandler, bool) {
	zapRegistryMu.RLock()
	defer zapRegistryMu.RUnlock()
	prefixes := make([]string, 0, len(zapGatewayRegistry))
	for p := range zapGatewayRegistry {
		prefixes = append(prefixes, p)
	}
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	for _, p := range prefixes {
		if prefixMatch(p, path) {
			return zapGatewayRegistry[p], true
		}
	}
	return nil, false
}

// lookupGatewayRoute returns the HTTP-shaped handler whose registered prefix is
// the longest match for path.
func lookupGatewayRoute(path string) (zapGatewayHandler, bool) {
	zapRegistryMu.RLock()
	defer zapRegistryMu.RUnlock()
	routes := make([]zapGatewayRoute, len(zapGatewayRoutes))
	copy(routes, zapGatewayRoutes)
	sort.Slice(routes, func(i, j int) bool { return len(routes[i].prefix) > len(routes[j].prefix) })
	for _, r := range routes {
		if prefixMatch(r.prefix, path) {
			return r.handle, true
		}
	}
	return nil, false
}
