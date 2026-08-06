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

// Canonical ZAP native dispatch registry — the ONE seam every route-group
// self-registers into from its own init(). Populated by the zap_<group>.go
// files; consulted by handleCloudService (MsgType 100) and the gateway handler
// (MsgType 200) in zap_native.go.
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
//     registerGatewayRoute / lookupGatewayRoutes.
//
// Both gateway registries resolve by LONGEST matching prefix, so a specific
// route ("/v1/admin/providers/toggle") always beats a broader sibling
// ("/v1/admin/providers"). The gateway handler consults the HTTP-shaped registry
// first, then the body-only one.
//
// A prefix is a claim on a SUBTREE, though, and a subtree holds siblings that
// belong to other registrations — "/v1/models/" carries the {model}/access
// routes while "/v1/models/providers" is its own endpoint. So the rule is the
// longest match that CLAIMS the path: an HTTP-shaped handler returns errDecline
// for a path inside its prefix it does not serve, and dispatch keeps looking
// (next candidate, then the body-only registry, then the router below).
//
// A gateway path no group claims is not a hole. Both lookups missing sends the
// request to serveGatewayViaRouter (zap_gateway_fallback.go), which replays it
// in-process through routers.App — the one router that resolves method + path
// parameters and runs every filter. A miss becomes a 404 only when no router is
// available to fall back to.
//
// The cloud registry has no such fallback, and cannot: a MsgType 100 method name
// is not a URL, so there is no route for a router to resolve. An unregistered
// method is a 404.

package controllers

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/luxfi/zap"
)

// errDecline is the "not mine" answer an HTTP-shaped gateway handler returns for
// a path that fell inside its registered prefix but is not one of the routes it
// serves. dispatchGateway then keeps looking instead of answering 404, which is
// what makes a broad prefix safe to register: it costs nothing it does not
// actually serve.
//
// Only the HTTP-shaped shape can decline — a body-only handler is never told the
// path, so it has nothing to decline on.
var errDecline = errors.New("zap: handler declines path")

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

// lookupGatewayRoutes returns every HTTP-shaped handler whose registered prefix
// matches path, longest prefix first. Dispatch runs them in that order and the
// first that does not return errDecline answers — so the winner is the longest
// prefix that CLAIMS the path, not merely the longest that contains it.
func lookupGatewayRoutes(path string) []zapGatewayHandler {
	zapRegistryMu.RLock()
	defer zapRegistryMu.RUnlock()
	routes := make([]zapGatewayRoute, len(zapGatewayRoutes))
	copy(routes, zapGatewayRoutes)
	sort.Slice(routes, func(i, j int) bool { return len(routes[i].prefix) > len(routes[j].prefix) })
	var out []zapGatewayHandler
	for _, r := range routes {
		if prefixMatch(r.prefix, path) {
			out = append(out, r.handle)
		}
	}
	return out
}
