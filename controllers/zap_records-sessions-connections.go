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

// Native ZAP handlers for the records / sessions / connections route-group —
// the STRANGLER twin of controllers/{session,connection,record,record_chain}.go.
//
// These are HTTP-shaped org-scoped admin/CRUD routes (query strings, GET vs POST,
// owner scoping via the authenticated principal), so — like the model-routing
// group — they ride the gateway HTTP-over-ZAP projection (MsgType 200):
// method(0) + path(8) + headers(16) + body(24) + query(32). The handlers here
// re-implement the beego controllers' logic against object/ + iam directly; they
// NEVER wrap or transform the beego controller.
//
// Registration follows the per-group convention: this file self-registers its
// path prefixes from its OWN init() (registerZapRecordsSessionsConnections) into
// the shared registry — no edit to zap_native.go or any shared registration file.
// The beego routes in router.go keep serving in parallel until Integrate flips the
// gateway dispatch to consult the registry.

package controllers

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/iam-v1"
	"github.com/luxfi/zap"
)

func init() { registerZapRecordsSessionsConnections() }

// registerZapRecordsSessionsConnections wires this group's routes into the shared
// registry. Named so InitZapHandlers can also call it explicitly if init-order
// ever needs pinning — InitZapHandlers gains no per-group switch arm.
func registerZapRecordsSessionsConnections() {
	// Sessions.
	registerGatewayRoute("/v1/get-sessions", zapSessionsHandler)
	registerGatewayRoute("/v1/get-session", zapSessionsHandler)
	registerGatewayRoute("/v1/update-session", zapSessionsHandler)
	registerGatewayRoute("/v1/add-session", zapSessionsHandler)
	registerGatewayRoute("/v1/delete-session", zapSessionsHandler)
	registerGatewayRoute("/v1/is-session-duplicated", zapSessionsHandler)

	// Connections.
	registerGatewayRoute("/v1/get-connections", zapConnectionsHandler)
	registerGatewayRoute("/v1/get-connection", zapConnectionsHandler)
	registerGatewayRoute("/v1/update-connection", zapConnectionsHandler)
	registerGatewayRoute("/v1/add-connection", zapConnectionsHandler)
	registerGatewayRoute("/v1/delete-connection", zapConnectionsHandler)
	registerGatewayRoute("/v1/start-connection", zapConnectionsHandler)
	registerGatewayRoute("/v1/stop-connection", zapConnectionsHandler)

	// Records.
	registerGatewayRoute("/v1/get-records", zapRecordsHandler)
	registerGatewayRoute("/v1/get-record", zapRecordsHandler)
	registerGatewayRoute("/v1/update-record", zapRecordsHandler)
	registerGatewayRoute("/v1/add-records", zapRecordsHandler)
	registerGatewayRoute("/v1/add-record", zapRecordsHandler)
	registerGatewayRoute("/v1/delete-record", zapRecordsHandler)
	registerGatewayRoute("/v1/commit-record-second", zapRecordsHandler)
	registerGatewayRoute("/v1/commit-record", zapRecordsHandler)
	registerGatewayRoute("/v1/query-record-second", zapRecordsHandler)
	registerGatewayRoute("/v1/query-record", zapRecordsHandler)
}

// ── Org scoping: the ONE tenant seam, mirroring ApiController.GetScopedOwner ─────
//
// The org is the authenticated principal's Owner — NEVER a client-supplied field.
// A signed-in non-admin is always scoped to their own org; only an `admin`-org
// principal may target another org via ?owner=. No verified principal → 401.
// Returns the resolved owner, or a refusal message to return verbatim.
func zapRSCScopedOwner(auth string, q url.Values) (string, *zap.Message) {
	user := zapPrincipalUser(auth)
	if user == nil {
		m, _ := zapGwError(401, "auth:Please sign in first")
		return "", m
	}
	if user.Owner == "admin" {
		if requested := strings.TrimSpace(q.Get("owner")); requested != "" {
			return requested, nil
		}
	}
	return user.Owner, nil
}

// zapRequirePrincipal mirrors RequireSignedInUser for the non-scoped routes
// (by-id fetches / body-driven writes): any verified principal is admitted; the
// object layer's own id parsing enforces the resource identity. No principal → 401.
func zapRequirePrincipal(auth string) (*iam.User, *zap.Message) {
	user := zapPrincipalUser(auth)
	if user == nil {
		m, _ := zapGwError(401, "auth:Please sign in first")
		return nil, m
	}
	return user, nil
}

// zapGwAction renders a *Response (wrapActionResponse / wrapActionResponse2 output)
// as a gateway 200 body — the beego action path is HTTP 200 with the status inside
// the envelope, so this mirrors ServeJSON exactly.
func zapGwAction(resp *Response) (*zap.Message, error) {
	b, _ := json.Marshal(resp)
	return object.BuildGatewayResponse(200, b, nil)
}

// ── Sessions (session.go) ───────────────────────────────────────────────────────

func zapSessionsHandler(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	q, _ := url.ParseQuery(query)
	switch path {
	case "/v1/get-sessions":
		return zapGetSessions(auth, q)
	case "/v1/get-session":
		return zapGetSingleSession(auth, q)
	case "/v1/update-session":
		return zapUpdateSession(auth, body)
	case "/v1/add-session":
		return zapAddSession(auth, body)
	case "/v1/delete-session":
		return zapDeleteSession(auth, body)
	case "/v1/is-session-duplicated":
		return zapIsSessionDuplicated(auth, q)
	}
	return zapGwError(404, "not found: "+path)
}

func zapGetSessions(auth string, q url.Values) (*zap.Message, error) {
	owner, refuse := zapRSCScopedOwner(auth, q)
	if refuse != nil {
		return refuse, nil
	}
	limit := q.Get("pageSize")
	page := q.Get("p")
	field := q.Get("field")
	value := q.Get("value")
	sortField := q.Get("sortField")
	sortOrder := q.Get("sortOrder")

	if limit == "" || page == "" {
		sessions, err := object.GetSessions(owner)
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(sessions)
	}
	lim := util.ParseInt(limit)
	count, err := object.GetSessionCount(owner, field, value)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	offset := zapPageOffset(page, lim)
	sessions, err := object.GetPaginationSessions(owner, offset, lim, field, value, sortField, sortOrder)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(sessions, count)
}

func zapGetSingleSession(auth string, q url.Values) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	session, err := object.GetSession(q.Get("sessionId"))
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(session)
}

func zapUpdateSession(auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var session object.Session
	if err := json.Unmarshal(body, &session); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwAction(wrapActionResponse(object.UpdateSession(util.GetIdFromOwnerAndName(session.Owner, session.Name), &session)))
}

func zapAddSession(auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var session object.Session
	if err := json.Unmarshal(body, &session); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwAction(wrapActionResponse(object.AddSession(&session)))
}

func zapDeleteSession(auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var session object.Session
	if err := json.Unmarshal(body, &session); err != nil {
		return zapGwError(200, err.Error())
	}
	if len(session.SessionId) == 0 {
		return zapGwError(200, "controllers:No sessions to delete")
	}
	return zapGwAction(wrapActionResponse(object.DeleteSession(util.GetIdFromOwnerAndName(session.Owner, session.Name))))
}

func zapIsSessionDuplicated(auth string, q url.Values) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	dup, err := object.IsSessionDuplicated(q.Get("sessionPkId"), q.Get("sessionId"))
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(dup)
}

// ── Connections (connection.go) ─────────────────────────────────────────────────

func zapConnectionsHandler(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	q, _ := url.ParseQuery(query)
	switch path {
	case "/v1/get-connections":
		return zapGetConnections(auth, q)
	case "/v1/get-connection":
		return zapGetConnection(auth, q)
	case "/v1/update-connection":
		return zapUpdateConnection(auth, q, body)
	case "/v1/add-connection":
		return zapAddConnection(auth, body)
	case "/v1/delete-connection":
		return zapDeleteConnection(auth, body)
	case "/v1/start-connection":
		return zapStartConnection(auth, q)
	case "/v1/stop-connection":
		return zapStopConnection(auth, q)
	}
	return zapGwError(404, "not found: "+path)
}

func zapGetConnections(auth string, q url.Values) (*zap.Message, error) {
	owner, refuse := zapRSCScopedOwner(auth, q)
	if refuse != nil {
		return refuse, nil
	}
	limit := q.Get("pageSize")
	page := q.Get("p")
	field := q.Get("field")
	value := q.Get("value")
	sortField := q.Get("sortField")
	sortOrder := q.Get("sortOrder")
	status := q.Get("status")

	if limit == "" || page == "" {
		connections, err := object.GetConnections(owner)
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(connections)
	}
	lim := util.ParseInt(limit)
	count, err := object.GetConnectionCount(owner, status, field, value)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	offset := zapPageOffset(page, lim)
	connections, err := object.GetPaginationConnections(owner, status, offset, lim, field, value, sortField, sortOrder)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(connections, count)
}

func zapGetConnection(auth string, q url.Values) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	connection, err := object.GetConnection(q.Get("id"))
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(connection)
}

func zapUpdateConnection(auth string, q url.Values, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var connection object.Connection
	if err := json.Unmarshal(body, &connection); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwAction(wrapActionResponse(object.UpdateConnection(q.Get("id"), &connection)))
}

func zapAddConnection(auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var connection object.Connection
	if err := json.Unmarshal(body, &connection); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwAction(wrapActionResponse(object.AddConnection(&connection)))
}

func zapDeleteConnection(auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var connection object.Connection
	if err := json.Unmarshal(body, &connection); err != nil {
		return zapGwError(200, err.Error())
	}
	affected, err := object.DeleteConnection(&connection)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwAction(wrapActionResponse(affected))
}

func zapStartConnection(auth string, q url.Values) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	connection := &object.Connection{
		Status:    object.Connected,
		StartTime: util.GetCurrentTime(),
	}
	if _, err := object.UpdateConnection(q.Get("id"), connection, []string{"status", "start_time"}...); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk()
}

func zapStopConnection(auth string, q url.Values) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	if err := object.CloseConnection(q.Get("id"), ForcedDisconnect, "The administrator forcibly closes the session"); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk()
}

// ── Records (record.go + record_chain.go) ───────────────────────────────────────

func zapRecordsHandler(ctx context.Context, method, path, query, auth string, body []byte) (*zap.Message, error) {
	q, _ := url.ParseQuery(query)
	switch path {
	case "/v1/get-records":
		return zapGetRecords(auth, q)
	case "/v1/get-record":
		return zapGetRecord(auth, q)
	case "/v1/update-record":
		return zapUpdateRecord(auth, q, body)
	case "/v1/add-record":
		return zapAddRecord(auth, body)
	case "/v1/add-records":
		return zapAddRecords(auth, q, body)
	case "/v1/delete-record":
		return zapDeleteRecord(auth, body)
	case "/v1/commit-record":
		return zapCommitRecord(auth, body)
	case "/v1/commit-record-second":
		return zapCommitRecordSecond(auth, body)
	case "/v1/query-record":
		return zapQueryRecord(auth, q)
	case "/v1/query-record-second":
		return zapQueryRecordSecond(auth, q)
	}
	return zapGwError(404, "not found: "+path)
}

func zapGetRecords(auth string, q url.Values) (*zap.Message, error) {
	owner, refuse := zapRSCScopedOwner(auth, q)
	if refuse != nil {
		return refuse, nil
	}
	limit := q.Get("pageSize")
	page := q.Get("p")
	field := q.Get("field")
	value := q.Get("value")
	sortField := q.Get("sortField")
	sortOrder := q.Get("sortOrder")

	if limit == "" || page == "" {
		records, err := object.GetRecords(owner)
		if err != nil {
			return zapGwError(200, err.Error())
		}
		return zapGwOk(records)
	}
	lim, err := util.ParseIntWithError(limit)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	count, err := object.GetRecordCount(owner, field, value)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	offset := zapPageOffset(page, lim)
	records, err := object.GetPaginationRecords(owner, offset, lim, field, value, sortField, sortOrder)
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(records, count)
}

func zapGetRecord(auth string, q url.Values) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	record, err := object.GetRecord(q.Get("id"), zapLang(q))
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(record)
}

func zapUpdateRecord(auth string, q url.Values, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var record object.Record
	if err := json.Unmarshal(body, &record); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwAction(wrapActionResponse(object.UpdateRecord(q.Get("id"), &record, zapLang(q))))
}

func zapAddRecord(auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var record object.Record
	if err := json.Unmarshal(body, &record); err != nil {
		return zapGwError(200, err.Error())
	}
	// Native path has no *http.Request; ClientIp/UserAgent come only from the body
	// (the beego path fills them from the request). Count defaults to 1 as in beego.
	if record.Count == 0 {
		record.Count = 1
	}
	return zapGwAction(wrapActionResponse2(object.AddRecord(&record, "en")))
}

func zapAddRecords(auth string, q url.Values, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	syncParam := strings.ToLower(q.Get("sync"))
	syncEnabled := syncParam == "true" || syncParam == "1"

	var records []*object.Record
	if err := json.Unmarshal(body, &records); err != nil {
		return zapGwError(200, err.Error())
	}
	if len(records) == 0 {
		return zapGwError(200, "controllers:No records to add")
	}
	for i := range records {
		if records[i].Count == 0 {
			records[i].Count = 1
		}
	}
	return zapGwAction(wrapActionResponse2(object.AddRecords(records, syncEnabled, "en")))
}

func zapDeleteRecord(auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var record object.Record
	if err := json.Unmarshal(body, &record); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwAction(wrapActionResponse(object.DeleteRecord(&record)))
}

func zapCommitRecord(auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var record object.Record
	if err := json.Unmarshal(body, &record); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwAction(wrapActionResponse2(object.CommitRecord(&record, "en")))
}

func zapCommitRecordSecond(auth string, body []byte) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	var record object.Record
	if err := json.Unmarshal(body, &record); err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwAction(wrapActionResponse(object.CommitRecordSecond(&record, "en")))
}

func zapQueryRecord(auth string, q url.Values) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	res, err := object.QueryRecord(q.Get("id"), zapLang(q))
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(res)
}

func zapQueryRecordSecond(auth string, q url.Values) (*zap.Message, error) {
	if _, refuse := zapRequirePrincipal(auth); refuse != nil {
		return refuse, nil
	}
	res, err := object.QueryRecordSecond(q.Get("id"), zapLang(q))
	if err != nil {
		return zapGwError(200, err.Error())
	}
	return zapGwOk(res)
}

// zapLang mirrors ApiController.GetAcceptLanguage for the native path: the gateway
// projection has no Accept-Language header seam, so honor an explicit ?language=
// override and default to "en" (parity with the other native ZAP handlers).
func zapLang(q url.Values) string {
	if lang := strings.TrimSpace(q.Get("language")); lang != "" {
		return lang
	}
	return "en"
}
