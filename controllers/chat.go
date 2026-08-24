// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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
	"fmt"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// chatOwner is the single tenant every chat-plane row lives under. A chat, its
// messages and its store are all keyed on it, and the plane reads it back verbatim:
// addInitialChat stamps it, GetChats queries it, the reply-to lookup and the
// provider and knowledge lookups all name it, and GetAnswer writes it. A row under
// any other owner is unreachable by the code that would answer it.
//
// It is also the ledger NAMESPACE an answer's debit lands in, and the first half of
// its billing SUBJECT. That is why it is a server-side constant: a request that
// could name it would be naming which tenant's wallet pays.
const chatOwner = "admin"

// GetGlobalChats
// @Title GetGlobalChats
// @Tag Chat API
// @Description get global chats
// @Success 200 {array} object.Chat The Response object
// @router /get-global-chats [get]
func (c *ApiController) GetGlobalChats() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	// A chat's tenant is its Organization; Owner is the namespace every chat
	// shares, so it says nothing about whose chat this is. The reserved org reads
	// every organization's, which is what reach answers with an empty string.
	org := reach(caller)

	limit := c.Input().Get("pageSize")
	page := c.Input().Get("p")
	field := c.Input().Get("field")
	value := c.Input().Get("value")
	sortField := c.Input().Get("sortField")
	sortOrder := c.Input().Get("sortOrder")
	store := c.Input().Get("store")

	if limit == "" || page == "" {
		chats, err := object.GetPaginationChats("", org, -1, -1, field, value, sortField, sortOrder, store)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(chats)
	} else {
		limit := util.ParseInt(limit)
		count, err := object.GetChatCount("", org, field, value, store)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		paginator := util.NewPaginator(c.PageAsked(), limit, count)
		chats, err := object.GetPaginationChats("", org, paginator.Offset(), limit, field, value, sortField, sortOrder, store)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(chats, paginator.Nums())
	}
}

// GetChats
// @Title GetChats
// @Tag Chat API
// @Description get chats
// @Param user query string true "The user of chat"
// @Param field query string true "The field of chat"
// @Param value query string true "The value of chat"
// @Param startTime query string false "Filter by start time"
// @Param endTime query string false "Filter by end time"
// @Success 200 {array} object.Chat The Response object
// @router /get-chats [get]
func (c *ApiController) GetChats() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}

	field := c.Input().Get("field")
	value := c.Input().Get("value")
	selectedUser := c.Input().Get("selectedUser")
	storeName := c.Input().Get("store")
	startTime := c.Input().Get("startTime")
	endTime := c.Input().Get("endTime")

	who := whose(caller, c.Input().Get("user"), selectedUser, field, value)

	// Apply store isolation based on user's Homepage field
	storeName, ok = c.EnforceStoreIsolation(storeName)
	if !ok {
		return
	}

	chats, err := object.GetChats(chatOwner, reach(caller), storeName, who)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Filter by time range if specified
	if startTime != "" || endTime != "" {
		chats = object.FilterChatsByTimeRange(chats, startTime, endTime)
	}

	c.ResponseOk(chats)
}

// GetChat
// @Title GetChat
// @Tag Chat API
// @Description get chat
// @Param id query string true "The id of chat"
// @Success 200 {object} object.Chat The Response object
// @router /get-chat [get]
func (c *ApiController) GetChat() {
	id := c.Input().Get("id")

	chat, err := object.GetChat(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if chat == nil || chat.Organization != c.GetOrg() {
		c.ResponseError("Chat not found")
		return
	}

	// Check if user has permission to view this chat
	if !c.IsAdmin() && !c.IsPreviewMode() {
		username := c.GetSessionUsername()
		if username != chat.User {
			c.ResponseForbidden(c.T("auth:Unauthorized operation"))
			return
		}
	}

	c.ResponseOk(chat)
}

// UpdateChat
// @Title UpdateChat
// @Tag Chat API
// @Description update Chat
// @Param id query string true "The id (owner/name) of the chat"
// @Param body body object.Chat true "The details of the chat"
// @Success 200 {object} controllers.Response The Response object
// @router /update-chat [post]
func (c *ApiController) UpdateChat() {
	id := c.Input().Get("id")

	var chat object.Chat
	err := json.Unmarshal(c.Body(), &chat)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	ok := c.IsCurrentUser(chat.User)
	if !ok {
		return
	}

	_, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	id = util.GetIdFromOwnerAndName(chatOwner, name)

	// The STORED row says whose chat this is. `chat.User` above is a value the request
	// CARRIES, so a caller that writes its own name there and someone else's chat in
	// ?id= passes that check and overwrites their row.
	originalChat, err := object.GetChat(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// A chat in another organization is not this caller's, and answers the way a
	// chat that is not there answers. The user check is not asked of an admin, and
	// an admin administers ONE organization.
	if originalChat == nil || originalChat.Organization != c.GetOrg() {
		c.ResponseError(fmt.Sprintf("The chat: %s is not found", id))
		return
	}
	if ok = c.IsCurrentUser(originalChat.User); !ok {
		return
	}

	// Organization is the org the usage plane books this chat's turns to
	// (recordCasibaseChatUsage and the TTS recorders read it, falling back to Owner),
	// so an update may no more move the row to another tenant's books than it may
	// change its owner.
	chat.Organization = originalChat.Organization

	if conf.IsDemoMode() {
		originalChat.ModelProvider = chat.ModelProvider
		chat = *originalChat
	}

	success, err := object.UpdateChat(id, &chat)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// AddChat
// @Title AddChat
// @Tag Chat API
// @Description add chat
// @Param body body object.Chat true "The details of the chat"
// @Success 200 {object} controllers.Response The Response object
// @router /add-chat [post]
func (c *ApiController) AddChat() {
	var chat object.Chat
	err := json.Unmarshal(c.Body(), &chat)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	ok := c.IsCurrentUser(chat.User)
	if !ok {
		return
	}

	chat.Owner = chatOwner
	// Organization is the org the usage plane books this chat's turns to. Like the
	// owner it is a fact about the caller, not a field the caller fills in.
	chat.Organization = c.GetOrg()

	currentTime := util.GetCurrentTime()
	chat.CreatedTime = currentTime
	chat.UpdatedTime = currentTime
	chat.ClientIp = c.getClientIp()
	chat.UserAgent = c.getUserAgent()
	chat.ClientIpDesc = util.GetDescFromIP(chat.ClientIp)
	chat.UserAgentDesc = util.GetDescFromUserAgent(chat.UserAgent)

	if chat.Store == "" {
		var store *object.Store
		store, err = object.GetDefaultStore("admin")
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		if store == nil {
			c.ResponseError(c.T("account:The default store is not found"))
			return
		}

		chat.Store = store.Name
	}

	success, err := object.AddChat(&chat)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// DeleteChat
// @Title DeleteChat
// @Tag Chat API
// @Description delete chat
// @Param body body object.Chat true "The details of the chat"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-chat [post]
func (c *ApiController) DeleteChat() {
	var chat object.Chat
	err := json.Unmarshal(c.Body(), &chat)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	ok := c.IsCurrentUser(chat.User)
	if !ok {
		return
	}

	chat.Owner = chatOwner

	// Authorize against the row being destroyed. The body names only WHICH chat
	// (owner + name); its `user` is another value the caller chose, so writing its own
	// name there and a victim's chat name in `name` satisfies the check above and
	// deletes the victim's chat and every message in it.
	storedChat, err := object.GetChat(chat.GetId())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// A chat in another organization is not this caller's, and answers the way a
	// chat that is not there answers. The user check is not asked of an admin, and
	// an admin administers ONE organization.
	if storedChat == nil || storedChat.Organization != c.GetOrg() {
		c.ResponseError(fmt.Sprintf("The chat: %s is not found", chat.GetId()))
		return
	}
	if ok = c.IsCurrentUser(storedChat.User); !ok {
		return
	}

	success, err := object.DeleteChat(&chat)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	message := object.Message{
		Owner: chat.Owner,
		Chat:  chat.Name,
	}
	success, err = object.DeleteMessagesByChat(&message)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}
