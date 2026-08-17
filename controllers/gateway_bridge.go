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
	"encoding/json"
	"net/http"

	"github.com/hanzoai/ai/object"
)

// RouterConfigBridge is the HTTP transport binding for the RESTful router-config
// nouns (/v1/router/{policy,defaults,ledger,rewards,artifact-meta} and
// /v1/org/settings[/list]). It dispatches IN-PROCESS through dispatchGateway —
// the SAME canonical ZAP gateway registry (zap_registry.go) that the MsgType 200
// handler serves over the gateway transport. The native ZAP
// handler is the ONE and ONLY implementation of these routes; this is purely the
// api.hanzo.ai HTTP binding, so there is NO controller twin to drift from and the
// split-brain the router refactor removed stays removed.
//
// Why a bridge and not a twin controller method: every other migrated route
// (get-records, get-connections, …) carries BOTH a controller method and a
// ZAP handler — the exact dual-impl drift that silently NULLed customer router
// settings (the update-router-policy data-wipe). Routing these nouns through the
// ZAP handler over one adapter keeps a single source of truth.
//
// Identity is the request's own Bearer credential (Authorization header), which
// the native handlers resolve exactly as the gateway does — every caller
// (console, chat, app) already sends it. The dispatched handler returns a ZAP
// message whose status is field 0 and body is field 4 (BuildCloudResponse /
// BuildGatewayResponse layout); both are relayed verbatim. The route is mapped
// "*" (any verb) because the native handler is method-aware: /v1/router/policy
// splits GET (read) vs PUT (write), /v1/org/settings GET/PUT/DELETE, and returns
// 405 for a verb it does not own.
func (c *ApiController) RouterConfigBridge() {
	path := c.Path()

	// nil router: this method IS a route inside routers.App, so it has nothing to
	// fall back to — replaying an unclaimed path through the router would dispatch
	// straight back here, forever. The registry is the only answer available at this
	// seam, which is exactly what the !handled arm below reports.
	msg, handled, err := dispatchGateway(
		c.Context(),
		nil,
		c.Method(),
		path,
		string(c.Fiber().Request().URI().QueryString()),
		c.Header("Authorization"),
		c.Body(),
	)
	switch {
	case err != nil:
		c.writeBridgeJSON(http.StatusInternalServerError, Response{Status: "error", Msg: err.Error()})
		return
	case !handled || msg == nil:
		// dispatchGateway reporting no owner means the registry and this route table
		// disagree — a wiring bug, not a client 404; surface it as 404 with the path.
		c.writeBridgeJSON(http.StatusNotFound, Response{Status: "error", Msg: "not found: " + path})
		return
	}

	root := msg.Root()
	status := int(root.Uint32(object.CloudRespStatus))
	if status == 0 {
		status = http.StatusOK
	}
	c.writeBridgeRaw(status, root.Bytes(object.CloudRespBody))
}

// writeBridgeRaw relays a status + already-encoded body verbatim, matching the
// respondAnthropicError direct-write idiom (Content-Type, WriteHeader, Body,
// render off) so the native handler's exact JSON/JSONL bytes reach the client.
func (c *ApiController) writeBridgeRaw(status int, body []byte) {
	c.SetHeader("Content-Type", "application/json; charset=utf-8")
	c.Status(status)
	_ = c.Bytes(http.StatusOK, body)
}

// writeBridgeJSON encodes and relays a bridge-local envelope (the only responses
// this adapter authors itself — dispatch wiring errors, never business logic).
func (c *ApiController) writeBridgeJSON(status int, resp Response) {
	body, _ := json.Marshal(resp)
	c.writeBridgeRaw(status, body)
}
