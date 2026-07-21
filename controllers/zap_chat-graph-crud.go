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

// Native ZAP handlers for the chat/message/graph CRUD route group (strangler
// migration of chat.go / message.go / graph.go / graph_chat.go). These
// re-implement the beego controller logic against object/ + iam directly — never
// wrapping the beego controller — mirroring controllers/zap_native.go:zapChatHandler
// and the first-migrated provider group (zap_providers-admin.go).
//
// Scope: this group is the CRUD surface only. The streaming answer-generation
// routes /v1/get-message-answer and /v1/get-answer live in message_answer.go
// (SSE over an http.ResponseWriter + session/store-scoped policy) and are a
// SEPARATE group; they are deliberately NOT migrated here.
//
// Auth parity: the native ZAP path never runs routers/authz_filter.go, so this
// file re-enforces that filter's decision verbatim in zapChatGraphAuthz for each
// route (preview-mode read bypass, the benign-read exempt list, and the org-admin
// gate for the non-exempt write routes), then each handler re-runs its OWN
// in-controller check (ownership via zapIsCurrentUser, store isolation via
// zapEnforceStoreIsolation, admin-only reads). The principal is resolved from the
// Bearer credential through the ONE shared seam (zapPrincipalUser: verified JWT
// via object.ParseAndValidateJWT / hk- IAM key via getUserByAccessKey) — identity
// is never taken from the body; org scope is always the resolved user's Owner.
//
// Envelope parity: success/error use the SAME {status,data,data2,msg} Response the
// beego ResponseOk/ResponseError emit (the console frontend contract), via the
// shared zapProviderOk / zapProviderError builders.
//
// Registration: this file OWNS its wiring. init() self-registers each route into
// the package-level registries (registerCloud / registerGatewayPath) born in
// zap_providers-admin.go; it never edits a shared registration file. Integrate
// flips handleCloudService / handleGatewayHTTPRequest to consult the registries
// once this group's handlers are proven; the beego routes in router.go stay live
// in parallel until then.

package controllers

import (
	"context"
	"encoding/json"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

func init() {
	registerZapChatGraphCrud()
}

// registerZapChatGraphCrud wires the group's native cloud methods (MsgType 100)
// and gateway path prefixes (MsgType 200) into the shared registries. Called by
// name so InitZapHandlers stays the single Node.Handle bootstrap without a
// per-group switch arm.
func registerZapChatGraphCrud() {
	// Cloud (MsgType 100) — native method names.
	registerCloud("chats.global.list", zapGetGlobalChatsHandler)
	registerCloud("chats.list", zapGetChatsHandler)
	registerCloud("chats.get", zapGetChatHandler)
	registerCloud("chats.update", zapUpdateChatHandler)
	registerCloud("chats.add", zapAddChatHandler)
	registerCloud("chats.delete", zapDeleteChatHandler)

	registerCloud("messages.global.list", zapGetGlobalMessagesHandler)
	registerCloud("messages.list", zapGetMessagesHandler)
	registerCloud("messages.get", zapGetMessageHandler)
	registerCloud("messages.update", zapUpdateMessageHandler)
	registerCloud("messages.add", zapAddMessageHandler)
	registerCloud("messages.delete", zapDeleteMessageHandler)
	registerCloud("messages.welcome.delete", zapDeleteWelcomeMessageHandler)

	registerCloud("graphs.global.list", zapGetGlobalGraphsHandler)
	registerCloud("graphs.list", zapGetGraphsHandler)
	registerCloud("graphs.get", zapGetGraphHandler)
	registerCloud("graphs.update", zapUpdateGraphHandler)
	registerCloud("graphs.add", zapAddGraphHandler)
	registerCloud("graphs.delete", zapDeleteGraphHandler)

	// Gateway (MsgType 200) — /v1 path prefixes routing to the SAME handlers.
	// dispatchGateway resolves by LONGEST matching prefix, so the pairs that share
	// a prefix (/v1/get-chat ⊂ /v1/get-chats, /v1/get-graph ⊂ /v1/get-graphs,
	// /v1/get-message ⊂ /v1/get-messages) route correctly. /v1/get-message-answer
	// (the streaming answer group's route) is longer than /v1/get-message and is
	// registered by THAT group, so once it migrates its longer prefix wins here.
	registerGatewayPath("/v1/get-global-chats", zapGetGlobalChatsHandler)
	registerGatewayPath("/v1/get-chats", zapGetChatsHandler)
	registerGatewayPath("/v1/get-chat", zapGetChatHandler)
	registerGatewayPath("/v1/update-chat", zapUpdateChatHandler)
	registerGatewayPath("/v1/add-chat", zapAddChatHandler)
	registerGatewayPath("/v1/delete-chat", zapDeleteChatHandler)

	registerGatewayPath("/v1/get-global-messages", zapGetGlobalMessagesHandler)
	registerGatewayPath("/v1/get-messages", zapGetMessagesHandler)
	registerGatewayPath("/v1/get-message", zapGetMessageHandler)
	registerGatewayPath("/v1/update-message", zapUpdateMessageHandler)
	registerGatewayPath("/v1/add-message", zapAddMessageHandler)
	registerGatewayPath("/v1/delete-welcome-message", zapDeleteWelcomeMessageHandler)
	registerGatewayPath("/v1/delete-message", zapDeleteMessageHandler)

	registerGatewayPath("/v1/get-global-graphs", zapGetGlobalGraphsHandler)
	registerGatewayPath("/v1/get-graphs", zapGetGraphsHandler)
	registerGatewayPath("/v1/get-graph", zapGetGraphHandler)
	registerGatewayPath("/v1/update-graph", zapUpdateGraphHandler)
	registerGatewayPath("/v1/add-graph", zapAddGraphHandler)
	registerGatewayPath("/v1/delete-graph", zapDeleteGraphHandler)
}

// ── Shared gates (parity with routers/authz_filter.go + base controller) ─────

// zapChatGraphAuthz re-enforces the beego authz filter's decision for this
// group's routes (none of which are super-admin or present-credential endpoints):
// a preview-mode read is open, a benign-read exempt route is open, and every other
// write route requires an ORG admin (util.IsAdmin) — a non-admin is denied 403,
// exactly like permissionFilter's denyForbidden tail. name is the controllerName
// (the beego path minus "/v1/"). Returns a denial message to return as-is, or nil
// to proceed to the handler's own in-controller check.
func zapChatGraphAuthz(name string, user *iam.User) *zap.Message {
	isGet := len(name) >= 4 && name[:4] == "get-"
	isWrite := hasAnyPrefix(name, "update-", "add-", "delete-")

	// Preview-mode default-open for reads (matches !disablePreviewMode && isGet).
	if !conf.DisablePreviewMode() && isGet {
		return nil
	}
	// A name that is neither a read nor a write is untouched by the filter.
	if !isGet && !isWrite {
		return nil
	}
	if _, ok := zapChatGraphExempt[name]; ok {
		return nil
	}
	if !util.IsAdmin(user) {
		msg, _ := zapProviderError(403, "this operation requires admin privilege")
		return msg
	}
	return nil
}

// zapChatGraphExempt mirrors the benign-read/self-scoped exempt set in
// permissionFilter for this group's routes: these skip the coarse org-admin gate
// because each handler performs its own ownership / signed-in check.
var zapChatGraphExempt = map[string]struct{}{
	"get-chats": {}, "get-chat": {},
	"update-chat": {}, "add-chat": {}, "delete-chat": {},
	"get-messages": {}, "get-message": {},
	"update-message": {}, "add-message": {}, "delete-welcome-message": {},
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}

// zapIsCurrentUser mirrors ApiController.IsCurrentUser: an org admin may act on any
// user's row; a non-admin may act only on its own (username == input). Returns a
// 403 denial to return as-is, or nil to proceed. The anonymous (u-<hash>) fallback
// of the beego path is unavailable natively (no client IP / user-agent), so a
// native caller is always its verified Bearer principal.
func zapIsCurrentUser(user *iam.User, input string) *zap.Message {
	username := ""
	if user != nil {
		username = user.Name
	}
	if !util.IsAdmin(user) && username != input {
		msg, _ := zapProviderError(403, "Unauthorized operation")
		return msg
	}
	return nil
}

// zapEnforceStoreIsolation mirrors ApiController.EnforceStoreIsolation: a user
// bound to a store via Homepage is forced to that store for an unscoped/"All"
// request and denied for a different one. Returns the enforced store name and nil,
// or ("", denial).
func zapEnforceStoreIsolation(user *iam.User, requested string) (string, *zap.Message) {
	if user == nil || user.Homepage == "" {
		return requested, nil
	}
	if requested == "" || requested == "All" {
		return user.Homepage, nil
	}
	if requested != user.Homepage {
		msg, _ := zapProviderError(200, "You can only access data from your assigned store")
		return "", msg
	}
	return requested, nil
}

// paginationOffset computes the DB offset the beego pagination.SetPaginator would,
// from the 1-based page and page size carried on the native body (the native
// contract has no URL query or response headers). Guards a non-positive page.
func paginationOffset(page, limit int) int {
	offset := (page - 1) * limit
	if offset < 0 {
		return 0
	}
	return offset
}

// ── chat.go parity ───────────────────────────────────────────────────────────

// zapListChatsRequest carries the list/pagination + filter params the beego GET
// handlers read from the URL query.
type zapListChatsRequest struct {
	PageSize     string `json:"pageSize"`
	Page         string `json:"p"`
	Field        string `json:"field"`
	Value        string `json:"value"`
	SortField    string `json:"sortField"`
	SortOrder    string `json:"sortOrder"`
	Store        string `json:"store"`
	User         string `json:"user"`
	SelectedUser string `json:"selectedUser"`
	StartTime    string `json:"startTime"`
	EndTime      string `json:"endTime"`
}

// zapGetGlobalChatsHandler mirrors ApiController.GetGlobalChats.
func zapGetGlobalChatsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("get-global-chats", user); deny != nil {
		return deny, nil
	}
	var req zapListChatsRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}

	if req.PageSize == "" || req.Page == "" {
		chats, err := object.GetGlobalChats()
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		return zapProviderOk(chats)
	}

	limit := util.ParseInt(req.PageSize)
	count, err := object.GetChatCount("", req.Field, req.Value, req.Store)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	chats, err := object.GetPaginationChats("", offset, limit, req.Field, req.Value, req.SortField, req.SortOrder, req.Store)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(chats, count)
}

// zapGetChatsHandler mirrors ApiController.GetChats (admin sees all users; a
// non-admin is scoped to its own user, store isolation enforced from Homepage,
// optional time-range filter).
func zapGetChatsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("get-chats", user); deny != nil {
		return deny, nil
	}
	var req zapListChatsRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}

	isAdmin := util.IsAdmin(user)
	u := req.User
	if isAdmin {
		u = ""
	}
	if req.SelectedUser != "" && req.SelectedUser != "null" && isAdmin {
		u = req.SelectedUser
	}
	if !isAdmin && u != req.SelectedUser && req.SelectedUser != "" {
		return zapProviderError(200, "You can only view your own chats")
	}

	storeName, deny := zapEnforceStoreIsolation(user, req.Store)
	if deny != nil {
		return deny, nil
	}

	var chats []*object.Chat
	var err error
	if req.Field == "user" {
		chats, err = object.GetChats("admin", storeName, req.Value)
	} else {
		chats, err = object.GetChats("admin", storeName, u)
	}
	if err != nil {
		return zapProviderError(200, err.Error())
	}

	if req.StartTime != "" || req.EndTime != "" {
		chats = object.FilterChatsByTimeRange(chats, req.StartTime, req.EndTime)
	}
	return zapProviderOk(chats)
}

// zapIDRequest carries a single id (URL query in the beego GET handlers).
type zapIDRequest struct {
	ID string `json:"id"`
}

// zapGetChatHandler mirrors ApiController.GetChat (ownership check for a non-admin
// outside preview mode).
func zapGetChatHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("get-chat", user); deny != nil {
		return deny, nil
	}
	var req zapIDRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}

	chat, err := object.GetChat(req.ID)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	if chat == nil {
		return zapProviderError(200, "Chat not found")
	}

	if !util.IsAdmin(user) && !conf.IsPreviewMode() {
		username := ""
		if user != nil {
			username = user.Name
		}
		if username != chat.User {
			return zapProviderError(403, "Unauthorized operation")
		}
	}
	return zapProviderOk(chat)
}

// zapUpdateChatRequest carries the id (URL query in HTTP) plus the chat payload.
type zapUpdateChatRequest struct {
	ID   string      `json:"id"`
	Chat object.Chat `json:"chat"`
}

// zapUpdateChatHandler mirrors ApiController.UpdateChat (self-or-admin; in demo
// mode only ModelProvider is mutable).
func zapUpdateChatHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("update-chat", user); deny != nil {
		return deny, nil
	}
	var req zapUpdateChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	chat := req.Chat
	if deny := zapIsCurrentUser(user, chat.User); deny != nil {
		return deny, nil
	}

	if conf.IsDemoMode() {
		originalChat, err := object.GetChat(req.ID)
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		if originalChat == nil {
			return zapProviderError(200, "The chat: "+req.ID+" is not found")
		}
		originalChat.ModelProvider = chat.ModelProvider
		chat = *originalChat
	}

	success, err := object.UpdateChat(req.ID, &chat)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// zapAddChatHandler mirrors ApiController.AddChat: self-or-admin, timestamps + IP/
// user-agent descriptors stamped from the (optional) body-supplied ClientIp/
// UserAgent, default store filled when absent.
func zapAddChatHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("add-chat", user); deny != nil {
		return deny, nil
	}
	var chat object.Chat
	if err := json.Unmarshal(body, &chat); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	if deny := zapIsCurrentUser(user, chat.User); deny != nil {
		return deny, nil
	}

	currentTime := util.GetCurrentTime()
	chat.CreatedTime = currentTime
	chat.UpdatedTime = currentTime
	chat.ClientIpDesc = util.GetDescFromIP(chat.ClientIp)
	chat.UserAgentDesc = util.GetDescFromUserAgent(chat.UserAgent)

	if chat.Store == "" {
		store, err := object.GetDefaultStore("admin")
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		if store == nil {
			return zapProviderError(200, "The default store is not found")
		}
		chat.Store = store.Name
	}

	success, err := object.AddChat(&chat)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// zapDeleteChatHandler mirrors ApiController.DeleteChat (deletes the chat and its
// messages).
func zapDeleteChatHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("delete-chat", user); deny != nil {
		return deny, nil
	}
	var chat object.Chat
	if err := json.Unmarshal(body, &chat); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	if deny := zapIsCurrentUser(user, chat.User); deny != nil {
		return deny, nil
	}

	success, err := object.DeleteChat(&chat)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	message := object.Message{Owner: chat.Owner, Chat: chat.Name}
	success, err = object.DeleteMessagesByChat(&message)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// ── message.go parity ────────────────────────────────────────────────────────

// zapListMessagesRequest carries the message list/pagination + filter params.
type zapListMessagesRequest struct {
	PageSize     string `json:"pageSize"`
	Page         string `json:"p"`
	Field        string `json:"field"`
	Value        string `json:"value"`
	SortField    string `json:"sortField"`
	SortOrder    string `json:"sortOrder"`
	Store        string `json:"store"`
	User         string `json:"user"`
	Chat         string `json:"chat"`
	SelectedUser string `json:"selectedUser"`
}

// zapGetGlobalMessagesHandler mirrors ApiController.GetGlobalMessages (signed-in +
// admin only; owner-scoped pagination).
func zapGetGlobalMessagesHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if user == nil {
		return zapProviderError(401, "Please sign in first")
	}
	if !util.IsAdmin(user) {
		return zapProviderError(403, "this operation requires admin privilege")
	}
	owner := user.Owner
	var req zapListMessagesRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}

	if req.PageSize == "" || req.Page == "" {
		messages, err := object.GetGlobalMessages()
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		return zapProviderOk(messages)
	}

	limit := util.ParseInt(req.PageSize)
	count, err := object.GetMessageCount(owner, req.Field, req.Value, req.Store)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	messages, err := object.GetPaginationMessages(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder, req.Store)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(messages, count)
}

// zapGetMessagesHandler mirrors ApiController.GetMessages (signed-in; admin sees
// all users, non-admin scoped to itself; a chat filter returns that chat's turns).
func zapGetMessagesHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if user == nil {
		return zapProviderError(401, "Please sign in first")
	}
	if deny := zapChatGraphAuthz("get-messages", user); deny != nil {
		return deny, nil
	}
	var req zapListMessagesRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}

	isAdmin := util.IsAdmin(user)
	u := req.User
	if isAdmin {
		u = ""
	}
	if req.SelectedUser != "" && req.SelectedUser != "null" && isAdmin {
		u = req.SelectedUser
	}
	if !isAdmin && u != req.SelectedUser && req.SelectedUser != "" {
		return zapProviderError(200, "You can only view your own messages")
	}

	if req.Chat == "" {
		messages, err := object.GetMessages(user.Owner, u, "")
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		return zapProviderOk(messages)
	}

	messages, err := object.GetChatMessages(req.Chat)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(messages)
}

// zapGetMessageHandler mirrors ApiController.GetMessage (ownership check for a
// non-admin outside preview mode).
func zapGetMessageHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("get-message", user); deny != nil {
		return deny, nil
	}
	var req zapIDRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}

	message, err := object.GetMessage(req.ID)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	if message == nil {
		return zapProviderError(200, "Message not found")
	}

	if !util.IsAdmin(user) && !conf.IsPreviewMode() {
		username := ""
		if user != nil {
			username = user.Name
		}
		if username != message.User {
			return zapProviderError(403, "Unauthorized operation")
		}
	}
	return zapProviderOk(message)
}

// zapUpdateMessageRequest carries the id + isHitOnly (URL query in HTTP) plus the
// message payload.
type zapUpdateMessageRequest struct {
	ID        string         `json:"id"`
	IsHitOnly bool           `json:"isHitOnly"`
	Message   object.Message `json:"message"`
}

// zapUpdateMessageHandler mirrors ApiController.UpdateMessage (self-or-admin;
// sends the notify email when requested, then persists).
func zapUpdateMessageHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("update-message", user); deny != nil {
		return deny, nil
	}
	var req zapUpdateMessageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	message := req.Message
	if deny := zapIsCurrentUser(user, message.User); deny != nil {
		return deny, nil
	}

	if message.NeedNotify {
		org := ""
		if user != nil {
			org = user.Owner
		}
		if err := message.SendEmail("en", org); err != nil {
			return zapProviderError(200, err.Error())
		}
		message.NeedNotify = false
	}

	success, err := object.UpdateMessage(req.ID, &message, req.IsHitOnly)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// zapAddMessageHandler mirrors ApiController.AddMessage: it edits-in-place (drops
// later turns), handles regeneration (drops the trailing AI+user turns), resolves
// or creates the parent chat, refines embedded files, enforces the store's
// forbidden-word list, persists the message, and — for an AI chat — seeds the
// empty AI answer placeholder the streaming answer route later fills. Returns the
// parent chat, exactly like the beego handler. lang is "en" and origin is empty
// natively (no http host) — file-URL refinement only rewrites base64-embedded
// images, which the native CRUD path does not carry.
func zapAddMessageHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("add-message", user); deny != nil {
		return deny, nil
	}
	var message object.Message
	if err := json.Unmarshal(body, &message); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	if deny := zapIsCurrentUser(user, message.User); deny != nil {
		return deny, nil
	}

	id := util.GetIdFromOwnerAndName(message.Owner, message.Name)
	originMessage, err := object.GetMessage(id)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	// If originMessage is non-nil this is an edit — drop all later messages.
	if originMessage != nil {
		if err = object.DeleteAllLaterMessages(id); err != nil {
			return zapProviderError(200, err.Error())
		}
	}

	addMessageAfterSuccess := true
	if message.IsRegenerated {
		messages, err := object.GetChatMessages(message.Chat)
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		var lastAIMessage *object.Message
		var lastUserMessage *object.Message
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Author == "AI" && messages[i].ErrorText != "" {
				lastAIMessage = messages[i]
				break
			}
		}
		if lastAIMessage == nil {
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Author == "AI" {
					lastAIMessage = messages[i]
					break
				}
			}
		}
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Author != "AI" {
				lastUserMessage = messages[i]
				break
			}
		}
		if lastAIMessage != nil {
			if lastAIMessage.ReplyTo == "Welcome" {
				message.Author = "AI"
				message.ReplyTo = "Welcome"
				addMessageAfterSuccess = false
			}
			if _, err = object.DeleteMessage(lastAIMessage); err != nil {
				return zapProviderError(200, err.Error())
			}
		}
		if lastUserMessage != nil {
			if _, err = object.DeleteMessage(lastUserMessage); err != nil {
				return zapProviderError(200, err.Error())
			}
		}
	}

	var chat *object.Chat
	if message.Chat == "" {
		chat, err = zapAddInitialChat(message.Organization, message.User, message.Store)
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		message.Organization = chat.Organization
		message.Chat = chat.Name
	} else {
		chatId := util.GetId(message.Owner, message.Chat)
		chat, err = object.GetChat(chatId)
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		if chat == nil {
			return zapProviderError(200, "chat:The chat: "+chatId+" is not found")
		}
	}

	if err = object.RefineMessageFiles(&message, "", "en"); err != nil {
		return zapProviderError(200, err.Error())
	}

	message.CreatedTime = util.GetCurrentTimeWithMilli()

	if message.Text == "" {
		return zapProviderError(200, "The question should not be empty")
	}

	storeId := util.GetId(message.Owner, message.Store)
	store, err := object.GetStore(storeId)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	if store != nil {
		contains, forbiddenWord := store.ContainsForbiddenWords(message.Text)
		if contains {
			return zapProviderError(200, "Your message contains a forbidden word: \""+forbiddenWord+"\"")
		}
	}

	success, err := object.AddMessage(&message)
	if err != nil {
		return zapProviderError(200, err.Error())
	}

	if success && addMessageAfterSuccess {
		chatId := util.GetId(message.Owner, message.Chat)
		chat, err = object.GetChat(chatId)
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		if chat != nil && chat.Type == "AI" {
			modelProvider := chat.ModelProvider
			if modelProvider == "" {
				storeId := util.GetId(chat.Owner, chat.Store)
				store, storeErr := object.GetStore(storeId)
				if storeErr == nil && store != nil {
					modelProvider = store.ModelProvider
				}
			}
			answerMessage := &object.Message{
				Owner:         message.Owner,
				Name:          "message_" + util.GetRandomName(),
				CreatedTime:   util.GetCurrentTimeEx(message.CreatedTime),
				Organization:  message.Organization,
				Store:         chat.Store,
				User:          message.User,
				Chat:          message.Chat,
				ReplyTo:       message.Name,
				Author:        "AI",
				Text:          "",
				FileName:      message.FileName,
				VectorScores:  []object.VectorScore{},
				ModelProvider: modelProvider,
			}
			if _, err = object.AddMessage(answerMessage); err != nil {
				return zapProviderError(200, err.Error())
			}
		}
	}

	return zapProviderOk(chat)
}

// zapAddMessageHandler reuses the shared zapAddInitialChat twin defined in
// zap_account-auth.go (identical store-resolve + AI-chat seed) — the ONE native
// initial-chat seeder, not a second copy.

// zapDeleteMessageHandler mirrors ApiController.DeleteMessage. This route is NOT
// on the beego filter's exempt list, so the outer gate requires an org admin.
func zapDeleteMessageHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("delete-message", user); deny != nil {
		return deny, nil
	}
	var message object.Message
	if err := json.Unmarshal(body, &message); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	success, err := object.DeleteMessage(&message)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// zapDeleteWelcomeMessageHandler mirrors ApiController.DeleteWelcomeMessage: only
// the message's own (signed-in) user may delete it, and only if it is the AI
// "Welcome" turn. The beego anonymous (u-<hash>) branch is unavailable natively
// (no client IP / user-agent) — a native caller is its verified Bearer principal.
func zapDeleteWelcomeMessageHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("delete-welcome-message", user); deny != nil {
		return deny, nil
	}
	var reqMessage object.Message
	if err := json.Unmarshal(body, &reqMessage); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}

	id := util.GetIdFromOwnerAndName(reqMessage.Owner, reqMessage.Name)
	message, err := object.GetMessage(id)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	if message == nil {
		return zapProviderError(200, "Message not found")
	}

	username := ""
	if user != nil {
		username = user.Name
	}
	if username == "" || username != message.User {
		return zapProviderError(200, "No permission")
	}

	if message.Author != "AI" || message.ReplyTo != "Welcome" {
		return zapProviderError(200, "No permission")
	}

	success, err := object.DeleteMessage(message)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// ── graph.go / graph_chat.go parity ──────────────────────────────────────────

// zapListGraphsRequest carries the graph list/pagination + filter params.
type zapListGraphsRequest struct {
	PageSize  string `json:"pageSize"`
	Page      string `json:"p"`
	Field     string `json:"field"`
	Value     string `json:"value"`
	SortField string `json:"sortField"`
	SortOrder string `json:"sortOrder"`
	Owner     string `json:"owner"`
}

// zapGetGlobalGraphsHandler mirrors ApiController.GetGlobalGraphs.
func zapGetGlobalGraphsHandler(_ context.Context, auth string, _ []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("get-global-graphs", user); deny != nil {
		return deny, nil
	}
	graphs, err := object.GetGlobalGraphs()
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(object.GetMaskedGraphs(graphs, true))
}

// zapGetGraphsHandler mirrors ApiController.GetGraphs: signed-in, owner resolved
// from the principal (a member of the admin org may target a specific ?owner).
func zapGetGraphsHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("get-graphs", user); deny != nil {
		return deny, nil
	}
	if user == nil {
		return zapProviderError(401, "Please sign in first")
	}
	var req zapListGraphsRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}

	owner := user.Owner
	if user.Owner == "admin" && req.Owner != "" {
		owner = req.Owner
	}

	if req.PageSize == "" || req.Page == "" {
		graphs, err := object.GetGraphs(owner)
		if err != nil {
			return zapProviderError(200, err.Error())
		}
		return zapProviderOk(object.GetMaskedGraphs(graphs, true))
	}

	limit := util.ParseInt(req.PageSize)
	count, err := object.GetGraphCount(owner, req.Field, req.Value)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	offset := paginationOffset(util.ParseInt(req.Page), limit)
	graphs, err := object.GetPaginationGraphs(owner, offset, limit, req.Field, req.Value, req.SortField, req.SortOrder)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(graphs, count)
}

// zapGetGraphHandler mirrors ApiController.GetGraph, including the lazy word-cloud
// generation for an empty "Chats"-category graph (generateChatGraphData, which
// touches only object/ — invoked on a zero controller since it ignores its
// receiver, reusing the ONE implementation rather than duplicating it).
func zapGetGraphHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("get-graph", user); deny != nil {
		return deny, nil
	}
	var req zapIDRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return zapProviderError(400, "invalid request: "+err.Error())
		}
	}

	graph, err := object.GetGraph(req.ID)
	if err != nil {
		return zapProviderError(200, err.Error())
	}

	if graph != nil && graph.Category == "Chats" && graph.Text == "" {
		if err = new(ApiController).generateChatGraphData(req.ID, graph); err != nil {
			return zapProviderError(200, err.Error())
		}
	}
	return zapProviderOk(object.GetMaskedGraph(graph, true))
}

// zapUpdateGraphRequest carries the id (URL query in HTTP) plus the graph payload.
type zapUpdateGraphRequest struct {
	ID    string       `json:"id"`
	Graph object.Graph `json:"graph"`
}

// zapUpdateGraphHandler mirrors ApiController.UpdateGraph (org-admin gated).
func zapUpdateGraphHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("update-graph", user); deny != nil {
		return deny, nil
	}
	var req zapUpdateGraphRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	success, err := object.UpdateGraph(req.ID, &req.Graph)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// zapAddGraphHandler mirrors ApiController.AddGraph (org-admin gated).
func zapAddGraphHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("add-graph", user); deny != nil {
		return deny, nil
	}
	var graph object.Graph
	if err := json.Unmarshal(body, &graph); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	success, err := object.AddGraph(&graph)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}

// zapDeleteGraphHandler mirrors ApiController.DeleteGraph (org-admin gated).
func zapDeleteGraphHandler(_ context.Context, auth string, body []byte) (*zap.Message, error) {
	user := zapPrincipalUser(auth)
	if deny := zapChatGraphAuthz("delete-graph", user); deny != nil {
		return deny, nil
	}
	var graph object.Graph
	if err := json.Unmarshal(body, &graph); err != nil {
		return zapProviderError(400, "invalid request: "+err.Error())
	}
	success, err := object.DeleteGraph(&graph)
	if err != nil {
		return zapProviderError(200, err.Error())
	}
	return zapProviderOk(success)
}
