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

// Native ZAP handlers for the infra / k8s CRUD route-group — the strangler
// migration of node.go / vm.go / the request-response leg of tunnel.go off
// beego. Cloud machine, image, container and pod provisioning is served by
// hanzoai/visor, which owns the compute domain.
//
// Pure ZAP handlers (no beego ApiController, no http.ResponseWriter): each
// re-implements its HTTP handler against object/ + iam, mirroring the recipe:
//   STEP 1 identity   — from the auth seam (zapResolveUser), NEVER the body.
//   STEP 2 org scope   — super admins target owner via the body's "owner"
//                        field (the ZAP twin of GetScopedOwner's query param);
//                        every other resolved identity is scoped to its own org.
//   STEP 3 decode      — the SAME field set the HTTP path reads (query → JSON body).
//   STEP 4 policy gate — the SAME util.IsSuperAdmin gate the routers authz_filter
//                        applies to every infra CRUD endpoint (superAdminEndpoints).
//   STEP 5 execute     — the object-layer primitives (GetMasked*, Sync*, Add/Update/Delete).
//   STEP 6 meter       — none: infra CRUD is not an LLM/billing path, so the beego
//                        controllers record no usage and neither do these (parity).
//   STEP 7 encode      — the SAME Response envelope the c.ResponseOk / wrapActionResponse
//                        HTTP path returns.
//
// Registration reuses the shared per-group registry (registerCloud /
// registerGatewayPath in zap_audio.go, zapOk/zapErr in zap_rag-search-crawl.go):
// this file only self-registers its own methods + path prefixes from its own
// init(); handleCloudService and the gateway dispatch consult that registry.
//
// TWO SPELLINGS, and this is the honest record of it: the prefixes registered
// below are the OLD flat names — /v1/get-nodes, /v1/add-node, /v1/add-node-tunnel,
// /v1/get-vm-dashboard-url. routers.App does not have those any more. The same
// surface is /v1/ai/nodes (and /:owner/:name, and /:owner/:name/tunnel) plus
// /v1/ai/dashboards/vm, generated from routers/resources.go. So a gateway caller
// and an HTTP caller reach these handlers by different names today. One noun,
// two spellings — a defect, not a design. It is written down rather than quietly
// normalised because dropping a prefix removes surface some caller may hold, and
// that is a decision with its own change, not a side effect of a comment.
//
// NOT migrated (deliberately): the duplex WebSocket tunnels — GetNodeTunnel and
// TunnelMonitor (tunnel.go, guacamole) and DevBridge (dev_bridge.go). A ZAP
// registry handler returns a SINGLE *zap.Message and holds no upgrade / duplex
// stream, so a persistent bidirectional tunnel cannot be expressed on it. Those
// stay on beego; only the request-response session-create leg AddNodeTunnel
// migrates here. tunnel_handler.go (the guacamole pump) is likewise untouched.

package controllers

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

func init() {
	registerZapInfraK8s()
}

// registerZapInfraK8s wires this group's native cloud methods (MsgType 100) and
// gateway path prefixes (MsgType 200) into the shared registry. Named so
// InitZapHandlers can also invoke it explicitly. Method names are the endpoint
// name (path minus "/v1/"); gateway prefixes are the exact "/v1/<name>" paths —
// longest-prefix resolution keeps "/v1/get-nodes" distinct from "/v1/get-node"
// and "/v1/add-node-tunnel" distinct from "/v1/add-node".
func registerZapInfraK8s() {
	pairs := []struct {
		method  string
		handler zapHandler
	}{
		{"get-nodes", zapGetNodesHandler},
		{"get-node", zapGetNodeHandler},
		{"add-node", zapAddNodeHandler},
		{"update-node", zapUpdateNodeHandler},
		{"delete-node", zapDeleteNodeHandler},

		{"add-node-tunnel", zapAddNodeTunnelHandler},
		{"get-vm-dashboard-url", zapGetVmDashboardUrlHandler},
	}
	for _, p := range pairs {
		registerCloud(p.method, p.handler)
		registerGatewayPath("/v1/"+p.method, p.handler)
	}
}

// ── Identity + policy seam (STEP 1 / STEP 2 / STEP 4) ────────────────────────

// zapInfraSuperAdmin resolves the principal STRICTLY from the auth token via the
// shared zapResolveUser seam (pk-/sk- IAM keys → getUserByAccessKey, JWTs →
// object.ParseAndValidateJWT), then enforces util.IsSuperAdmin — the SAME
// predicate the routers authz_filter applies to every infra CRUD endpoint. It is
// the controller-side RequireSuperAdmin twin for the stateless ZAP wire:
// fail-closed, no principal → 401, authenticated non-super-admin → 403. On
// success it returns the resolved (owner, name).
func zapInfraSuperAdmin(auth string) (owner, name string, reject *zap.Message) {
	if strings.TrimSpace(auth) == "" {
		reject, _ = object.BuildCloudResponse(401, nil, "authentication required")
		return "", "", reject
	}
	id, err := zapResolveUser(auth)
	if err != nil {
		reject, _ = object.BuildCloudResponse(401, nil, err.Error())
		return "", "", reject
	}
	owner = id
	if i := strings.IndexByte(id, '/'); i >= 0 {
		owner = id[:i]
		name = id[i+1:]
	}
	// Reconstruct the principal for the ONE super-admin predicate rather than
	// re-deriving the policy (owner == AdminOrg) inline.
	if !util.IsSuperAdmin(&iam.User{Owner: owner, Name: name}) {
		reject, _ = object.BuildCloudResponse(403, nil, "this operation requires super admin privilege")
		return "", "", reject
	}
	return owner, name, nil
}

// zapInfraScopedOwner is the ZAP twin of ApiController.GetScopedOwner for the
// super-admin case: the "admin" org may target a specific owner (the body's
// "owner" field, the query-param twin); absent that, the caller's own org.
func zapInfraScopedOwner(owner, requestedOwner string) string {
	if owner == util.AdminOrg {
		if r := strings.TrimSpace(requestedOwner); r != "" {
			return r
		}
	}
	return owner
}

// ── Request/response shapes (STEP 3 / STEP 7) ────────────────────────────────

// infraListParams is the field set the HTTP list handlers read from the query
// string; on the ZAP wire it arrives as the JSON body (the gateway folds its
// query string into the body — see the sibling groups' READ-PARAM note).
type infraListParams struct {
	PageSize  string `json:"pageSize"`
	P         string `json:"p"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	SortField string `json:"sortField"`
	SortOrder string `json:"sortOrder"`
	Owner     string `json:"owner"`
}

func decodeInfraListParams(body []byte) infraListParams {
	var p infraListParams
	_ = json.Unmarshal(body, &p)
	return p
}

// infraId extracts the "id" (owner/name) field a single-resource handler reads.
func infraId(body []byte) string {
	var p struct {
		Id string `json:"id"`
	}
	_ = json.Unmarshal(body, &p)
	return strings.TrimSpace(p.Id)
}

// paged reports whether the list handler should paginate (both pageSize + p set,
// mirroring the beego `limit == "" || page == ""` branch), and the derived
// (offset, limit) — offset = (page-1)*limit, the pagination.SetPaginator.Offset()
// value the HTTP path computes from the request.
func (p infraListParams) paged() (offset, limit int, ok bool) {
	if p.PageSize == "" || p.P == "" {
		return 0, 0, false
	}
	limit = util.ParseInt(p.PageSize)
	page := util.ParseInt(p.P)
	if page < 1 {
		page = 1
	}
	return (page - 1) * limit, limit, true
}

// zapOk2 encodes the two-value Response{status:ok,data,data2} the HTTP list
// handlers return via c.ResponseOk(data, count).
func zapOk2(data, data2 interface{}) (*zap.Message, error) {
	b, _ := json.Marshal(Response{Status: "ok", Data: data, Data2: data2})
	return object.BuildCloudResponse(200, b, "")
}

// zapActionResponse encodes wrapActionResponse — the exact envelope node.go /
// image.go return for add/update/delete (Affected / Unaffected / error).
func zapActionResponse(affected bool, err error) (*zap.Message, error) {
	b, _ := json.Marshal(wrapActionResponse(affected, err))
	return object.BuildCloudResponse(200, b, "")
}

// zapActionOk encodes machine.go / docker_container.go / kubernetes_pod.go's
// add/update/delete, which return c.ResponseOk(affected, err) verbatim (Data =
// affected, Data2 = err) — preserved exactly, including that shape.
func zapActionOk(affected bool, err error) (*zap.Message, error) {
	return zapOk2(affected, err)
}

// ── Nodes ────────────────────────────────────────────────────────────────────

func zapGetNodesHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	owner, _, reject := zapInfraSuperAdmin(auth)
	if reject != nil {
		return reject, nil
	}
	p := decodeInfraListParams(body)
	owner = zapInfraScopedOwner(owner, p.Owner)

	if offset, limit, ok := p.paged(); ok {
		count, err := object.GetNodeCount(owner, p.Field, p.Value)
		if err != nil {
			return zapErr(500, err.Error())
		}
		nodes, err := object.GetMaskedNodes(object.GetPaginationNodes(owner, offset, limit, p.Field, p.Value, p.SortField, p.SortOrder))
		if err != nil {
			return zapErr(500, err.Error())
		}
		return zapOk2(nodes, count)
	}
	nodes, err := object.GetMaskedNodes(object.GetNodes(owner))
	if err != nil {
		return zapErr(500, err.Error())
	}
	return zapOk(nodes)
}

func zapGetNodeHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, _, reject := zapInfraSuperAdmin(auth); reject != nil {
		return reject, nil
	}
	node, err := object.GetMaskedNode(object.GetNode(infraId(body)))
	if err != nil {
		return zapErr(500, err.Error())
	}
	return zapOk(node)
}

func zapAddNodeHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, _, reject := zapInfraSuperAdmin(auth); reject != nil {
		return reject, nil
	}
	var node object.Node
	if err := json.Unmarshal(body, &node); err != nil {
		return zapErr(400, err.Error())
	}
	return zapActionResponse(object.AddNode(&node))
}

func zapUpdateNodeHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, _, reject := zapInfraSuperAdmin(auth); reject != nil {
		return reject, nil
	}
	var node object.Node
	if err := json.Unmarshal(body, &node); err != nil {
		return zapErr(400, err.Error())
	}
	return zapActionResponse(object.UpdateNode(infraId(body), &node))
}

func zapDeleteNodeHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, _, reject := zapInfraSuperAdmin(auth); reject != nil {
		return reject, nil
	}
	var node object.Node
	if err := json.Unmarshal(body, &node); err != nil {
		return zapErr(400, err.Error())
	}
	return zapActionResponse(object.DeleteNode(&node))
}

// ── Node tunnel (session create) ─────────────────────────────────────────────

// zapAddNodeTunnelHandler is the request-response leg of tunnel.go's
// AddNodeTunnel: it creates the guacamole connection session. Unlike the CRUD
// endpoints it is NOT super-admin gated (the authz_filter lists it as a
// signed-in-user endpoint) — it requires only a resolvable principal. The
// duplex GetNodeTunnel WebSocket that consumes the returned session stays on
// beego. ClientIp/UserAgent are unset on the ZAP wire (no HTTP request context);
// Creator is the authenticated user, never a body-supplied field.
func zapAddNodeTunnelHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	id, err := zapResolveUser(auth)
	if err != nil {
		return zapErr(401, err.Error())
	}
	name := id
	if i := strings.IndexByte(id, '/'); i >= 0 {
		name = id[i+1:]
	}

	var req struct {
		NodeId string `json:"nodeId"`
		Mode   string `json:"mode"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapErr(400, err.Error())
		}
	}

	connection := &object.Connection{Creator: name}
	connection, err = object.CreateConnection(connection, req.NodeId, req.Mode)
	if err != nil {
		return zapErr(500, err.Error())
	}
	return zapOk(connection)
}

// ── VM dashboard URL ─────────────────────────────────────────────────────────

// zapGetVmDashboardUrlHandler mirrors vm.go's GetVmDashboardUrl — returns the VM
// control-plane dashboard URL from VM_DASHBOARD_URL (default localhost). It
// requires a resolvable principal (the recipe's mandatory auth seam); the value
// is otherwise identical to the HTTP path.
func zapGetVmDashboardUrlHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	if _, err := zapResolveUser(auth); err != nil {
		return zapErr(401, err.Error())
	}
	dashboardUrl := os.Getenv("VM_DASHBOARD_URL")
	if dashboardUrl == "" {
		dashboardUrl = "http://localhost:19000"
	}
	return zapOk(dashboardUrl)
}
