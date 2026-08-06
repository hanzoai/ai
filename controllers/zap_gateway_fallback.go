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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

// The ad-hoc gateway registry (MsgType 200, zap_registry.go) is a SECOND router:
// a hand-maintained map of path prefixes to handlers, matched longest-prefix on
// the path ALONE. That was workable only while every route encoded its verb in
// its name — /v1/get-store versus /v1/update-store are different prefixes, so a
// path was a complete request description.
//
// It stops being workable the moment the surface is RESTful. /v1/rag/stores/:owner/:name
// answers GET, PATCH and DELETE at one path, and prefix matching cannot tell them
// apart; worse, /v1/rag/stores/acme/thing prefix-matches /v1/rag/stores and would
// silently reach the LIST handler with the member's segments discarded. A wrong
// handler, not an error.
//
// Rather than teach the second router about verbs and path parameters — two
// routers to keep in agreement forever — a miss now falls through to the FIRST
// one. routers.App already resolves method + path + parameters and runs every
// filter (auth, tenant, balance); serving the bridged request through it is the
// same thing InitForwardBridge does for MsgTypeForward, and the same thing cloud's
// zapface dispatcher does for its own ZAP surface. One router, one answer.
//
// So the registry is an OPT-IN, not a migration ladder. A group registers a
// prefix when skipping the HTTP machinery buys something — the hot inference
// paths — and every path it does not register is the router's, permanently.
// Nothing here is waiting to be "flipped" from one dispatcher to the other, and
// the RESTful families could not be flipped even if something were: a resource
// answers four verbs at /v1/<ns>/<path>/:owner/:name, which is one prefix.
//
// This is the invariant the whole file exists to state. Read it before adding a
// prefix, and cite it instead of restating it.

var (
	gatewayFallbackMu sync.RWMutex
	gatewayFallback   http.Handler
)

// SetGatewayFallback installs the HTTP handler that serves MsgType 200 requests
// no registry entry claims. Pass routers.App — the fully-wrapped native router.
//
// Injected rather than imported because routers imports controllers; this is the
// same seam InitForwardBridge uses.
func SetGatewayFallback(h http.Handler) {
	gatewayFallbackMu.Lock()
	defer gatewayFallbackMu.Unlock()
	gatewayFallback = h
}

func gatewayFallbackHandler() http.Handler {
	gatewayFallbackMu.RLock()
	defer gatewayFallbackMu.RUnlock()
	return gatewayFallback
}

// serveGatewayViaRouter replays a MsgType 200 request through the native router
// and packs the response back into a ZAP message. Reports handled=false only when
// no fallback is installed, so a caller with no router configured keeps its 404.
func serveGatewayViaRouter(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, bool, error) {
	h := gatewayFallbackHandler()
	if h == nil {
		return nil, false, nil
	}
	if method == "" {
		method = http.MethodGet
	}
	target := path
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(body)).WithContext(ctx)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	out, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	// Carry the response headers so a caller sees the same content type and any
	// auth challenge it would have seen over plain HTTP.
	var headers []byte
	if len(res.Header) > 0 {
		flat := make(map[string]string, len(res.Header))
		for k := range res.Header {
			flat[k] = res.Header.Get(k)
		}
		headers, _ = json.Marshal(flat)
	}
	msg, err := object.BuildGatewayResponse(uint32(res.StatusCode), out, headers)
	return msg, true, err
}
