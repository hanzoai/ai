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

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// GetGlobalMessages
// @Title GetGlobalMessages
// @Tag Message API
// @Description get global messages
// @Success 200 {array} object.Message The Response object
// @router /get-global-messages [get]
func (c *ApiController) GetGlobalMessages() {
	// This reads every organization's messages, so it asks whether the caller is a
	// platform admin — membership of the reserved org. IsAdmin answers a narrower
	// question, "does this person administer their OWN org", which every customer's
	// owner also answers yes to.
	if !c.RequireSuperAdmin() {
		return
	}

	owner := c.GetSessionOwner()
	limit := c.Input().Get("pageSize")
	page := c.Input().Get("p")
	field := c.Input().Get("field")
	value := c.Input().Get("value")
	sortField := c.Input().Get("sortField")
	sortOrder := c.Input().Get("sortOrder")
	store := c.Input().Get("store")

	if limit == "" || page == "" {
		messages, err := object.GetGlobalMessages()
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		c.ResponseOk(messages)
	} else {
		limit := util.ParseInt(limit)
		count, err := object.GetMessageCount(owner, field, value, store)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		paginator := util.NewPaginator(c.PageAsked(), limit, count)
		messages, err := object.GetPaginationMessages(owner, paginator.Offset(), limit, field, value, sortField, sortOrder, store)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		c.ResponseOk(messages, paginator.Nums())
	}
}

// GetMessages
// @Title GetMessages
// @Tag Message API
// @Description get Messages
// @Param user query string true "The user of message"
// @Param chat query string true "The chat of message"
// @Success 200 {array} object.Message The Response object
// @router /get-Messages [get]
func (c *ApiController) GetMessages() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}

	chat := c.Input().Get("chat")
	selectedUser := c.Input().Get("selectedUser")

	who := whose(caller, c.Input().Get("user"), selectedUser, "", "")

	if chat == "" {
		messages, err := object.GetMessages(chatOwner, reach(caller), who, "")
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		c.ResponseOk(messages)
		return
	}

	// A chat name addresses a conversation in any tenant, so the transcript is
	// confined to the caller's org rather than served on the name alone.
	messages, err := object.GetChatMessages(chat, reach(caller))
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(messages)
}

// GetMessage
// @Title GetMessage
// @Tag Message API
// @Description get message
// @Param id query string true "The id of message"
// @Success 200 {object} object.Message The Response object
// @router /get-message [get]
func (c *ApiController) GetMessage() {
	id := c.Input().Get("id")

	message, err := object.GetMessage(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if message == nil {
		c.ResponseError("Message not found")
		return
	}

	// Check if user has permission to view this message
	if !c.IsAdmin() && !c.IsPreviewMode() {
		username := c.GetSessionUsername()
		if username != message.User {
			c.ResponseForbidden(c.T("auth:Unauthorized operation"))
			return
		}
	}

	c.ResponseOk(message)
}

// UpdateMessage
// @Title UpdateMessage
// @Tag Message API
// @Description update message
// @Param id query string true "The id (owner/name) of the message"
// @Param body body object.Message true "The details of the message"
// @Success 200 {object} controllers.Response The Response object
// @router /update-message [post]
func (c *ApiController) UpdateMessage() {
	id := c.Input().Get("id")
	isHitOnly := c.Input().Get("isHitOnly")

	var message object.Message
	err := json.Unmarshal(c.Body(), &message)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	ok := c.IsCurrentUser(message.User)
	if !ok {
		return
	}

	_, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	id = util.GetIdFromOwnerAndName(chatOwner, name)
	message.Owner = chatOwner

	// The STORED row says whose turn this is. `message.User` above is a value the
	// request CARRIES, so it cannot be what authorizes writing over someone else's.
	storedMessage, err := object.GetMessage(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// Which organization the turn belongs to. The user check below is not asked of
	// an admin, so this is what keeps an admin inside their own.
	if storedMessage == nil || storedMessage.Organization != c.GetOrg() {
		c.ResponseError(fmt.Sprintf("The message: %s is not found", id))
		return
	}
	if ok = c.IsCurrentUser(storedMessage.User); !ok {
		return
	}
	// A turn's org is the org the usage plane books it to; an update does not move it.
	message.Organization = storedMessage.Organization

	if message.NeedNotify {
		err = message.SendEmail(c.GetAcceptLanguage(), c.GetOrg())
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		message.NeedNotify = false
	}

	success, err := object.UpdateMessage(id, &message, isHitOnly == "true")
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

// AddMessage
// @Title AddMessage
// @Tag Message API
// @Description add message
// @Param body body object.Message true "The details of the message"
// @Success 200 {object} object.Chat The Response object
// @router /add-message [post]
func (c *ApiController) AddMessage() {
	var message object.Message
	err := json.Unmarshal(c.Body(), &message)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	ok := c.IsCurrentUser(message.User)
	if !ok {
		return
	}

	message.Owner = chatOwner

	// The organization this request acts in, settled once from the caller. It is
	// what a new chat is filed under just below, so it is also what says which
	// existing chats this request may touch — Owner is the namespace every chat
	// shares and names none of them.
	org := c.GetOrg()

	id := util.GetIdFromOwnerAndName(message.Owner, message.Name)
	originMessage, err := object.GetMessage(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// if originMessage not nil, means edit message, delete all later messages
	if originMessage != nil {
		// An edit rewrites a STORED turn and drops every turn after it, so the row's
		// own user authorizes it — not the user this request carries.
		if ok = c.IsCurrentUser(originMessage.User); !ok {
			return
		}
		err = object.DeleteAllLaterMessages(id)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
	}

	addMessageAfterSuccess := true
	if message.IsRegenerated {
		// Whose chat this is comes from the caller, not the body. This branch deletes
		// the two turns it replaces, and the organization it reads with is what says
		// which chat of that name it reads — an empty one asks every organization.
		// The body's Organization is not settled until the chat below is resolved.
		messages, err := object.GetChatMessages(message.Chat, org)
		if err != nil {
			c.ResponseError(err.Error())
			return
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
			_, err = object.DeleteMessage(lastAIMessage)
			if err != nil {
				c.ResponseError(err.Error())
				return
			}
		}
		if lastUserMessage != nil {
			_, err = object.DeleteMessage(lastUserMessage)
			if err != nil {
				c.ResponseError(err.Error())
				return
			}
		}
	}
	var chat *object.Chat
	if message.Chat == "" {
		chat, err = c.addInitialChat(org, message.User, message.Store)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		message.Chat = chat.Name
	} else {
		chatId := util.GetId(message.Owner, message.Chat)
		chat, err = object.GetChat(chatId)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		// A chat of this name in another organization is not this caller's to add a
		// turn to, and answers the way a chat of that name that is not there answers.
		if chat == nil || chat.Organization != org {
			c.ResponseError(fmt.Sprintf("chat:The chat: %s is not found", chatId))
			return
		}
	}
	// A turn's org is its CHAT's org — the chat is the row the usage plane reads, and
	// a turn cannot be booked to a different tenant than the conversation it is in.
	message.Organization = chat.Organization

	host := c.Host()
	origin := getOriginFromHost(host)
	err = object.RefineMessageFiles(&message, origin, c.GetAcceptLanguage())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	message.CreatedTime = util.GetCurrentTimeWithMilli()

	if message.Text == "" {
		c.ResponseError(fmt.Sprintf("The question should not be empty for message: %v", message))
		return
	}

	// Check for forbidden words
	storeId := util.GetId(message.Owner, message.Store)
	store, err := object.GetStore(storeId)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if store != nil {
		contains, forbiddenWord := store.ContainsForbiddenWords(message.Text)
		if contains {
			c.ResponseError(fmt.Sprintf("Your message contains a forbidden word: \"%s\"", forbiddenWord))
			return
		}
	}

	success, err := object.AddMessage(&message)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if success && addMessageAfterSuccess {
		chatId := util.GetId(message.Owner, message.Chat)
		chat, err = object.GetChat(chatId)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
		if chat != nil && chat.Type == "AI" {
			modelProvider := chat.ModelProvider
			if modelProvider == "" {
				// Fallback to store's model provider if chat doesn't have one
				storeId := util.GetId(chat.Owner, chat.Store)
				store, storeErr := object.GetStore(storeId)
				if storeErr == nil && store != nil {
					modelProvider = store.ModelProvider
				}
			}
			answerMessage := &object.Message{
				Owner:         message.Owner,
				Name:          fmt.Sprintf("message_%s", util.GetRandomName()),
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
			_, err = object.AddMessage(answerMessage)
			if err != nil {
				c.ResponseError(err.Error())
				return
			}
		}
	}

	c.ResponseOk(chat)
}

// DeleteMessage
// @Title DeleteMessage
// @Tag Message API
// @Description delete message
// @Param body body object.Message true "The details of the message"
// @Success 200 {object} controllers.Response The Response object
// @router /delete-message [post]
func (c *ApiController) DeleteMessage() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	var message object.Message
	err := json.Unmarshal(c.Body(), &message)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	message.Owner = chatOwner

	// Owner is the namespace every message shares, so it says nothing about whose
	// message this is — Organization does. The stored row is what carries it; the
	// body carries whatever it likes.
	stored, err := object.GetMessage(message.GetId())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if stored == nil || !reaches(caller, stored.Organization) {
		c.ResponseError(c.T("general:The message does not exist"))
		return
	}

	success, err := object.DeleteMessage(&message)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(success)
}

func (c *ApiController) DeleteWelcomeMessage() {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	var message *object.Message
	err := json.Unmarshal(c.Body(), &message)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if message == nil {
		c.ResponseError(c.T("general:The message does not exist"))
		return
	}
	// Organization is what says whose message this is; Owner is the namespace they
	// all share.
	stored, err := object.GetMessage(message.GetId())
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if stored == nil || !reaches(caller, stored.Organization) {
		c.ResponseError(c.T("general:The message does not exist"))
		return
	}

	id := util.GetIdFromOwnerAndName(chatOwner, message.Name)
	message, err = object.GetMessage(id)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	// GetMessage answers (nil, nil) for an id no row matches, so a miss arrives here as
	// a nil message rather than as an error. Reading message.User off it panics the
	// process, and this route serves callers with no session at all.
	if message == nil {
		c.ResponseError(fmt.Sprintf("The message: %s is not found", id))
		return
	}

	user := c.GetSessionUsername()
	if user != "" && user != message.User {
		c.ResponseError(c.T("controllers:No permission"))
		return
	}

	if user == "" {
		clientIp := c.getClientIp()
		userAgent := c.getUserAgent()
		hash := getContentHash(fmt.Sprintf("%s|%s", clientIp, userAgent))
		username := fmt.Sprintf("u-%s", hash)
		if username != message.User {
			c.ResponseError(c.T("controllers:No permission"))
			return
		}
	}

	if message.Author != "AI" || message.ReplyTo != "Welcome" {
		c.ResponseError(c.T("controllers:No permission"))
		return
	}

	success, err := object.DeleteMessage(message)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(success)
}
