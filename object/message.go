// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package object

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/model"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)

type VectorScore struct {
	Vector string  `json:"vector"`
	Score  float32 `json:"score"`
}
type Suggestion struct {
	Text  string `json:"text"`
	IsHit bool   `json:"isHit"`
}
type Message struct {
	Owner             string               `db:"pk" json:"owner"`
	Name              string               `db:"pk" json:"name"`
	CreatedTime       string               `json:"createdTime"`
	Organization      string               `json:"organization"`
	Store             string               `json:"store"`
	User              string               `json:"user"`
	Chat              string               `json:"chat"`
	ReplyTo           string               `json:"replyTo"`
	Author            string               `json:"author"`
	Text              string               `json:"text"`
	ReasonText        string               `json:"reasonText"`
	ErrorText         string               `json:"errorText"`
	FileName          string               `json:"fileName"`
	Comment           string               `json:"comment"`
	TokenCount        int                  `json:"tokenCount"`
	TextTokenCount    int                  `json:"textTokenCount"`
	Price             float64              `json:"price"`
	Currency          string               `json:"currency"`
	IsHidden          bool                 `json:"isHidden"`
	IsDeleted         bool                 `json:"isDeleted"`
	NeedNotify        bool                 `json:"needNotify"`
	IsAlerted         bool                 `json:"isAlerted"`
	IsRegenerated     bool                 `json:"isRegenerated"`
	WebSearchEnabled  bool                 `json:"webSearchEnabled"`
	ModelProvider     string               `json:"modelProvider"`
	EmbeddingProvider string               `json:"embeddingProvider"`
	VectorScores      []VectorScore        `json:"vectorScores"`
	LikeUsers         []string             `json:"likeUsers"`
	DisLikeUsers      []string             `json:"dislikeUsers"`
	Suggestions       []Suggestion         `json:"suggestions"`
	ToolCalls         []model.ToolCall     `json:"toolCalls"`
	SearchResults     []model.SearchResult `json:"searchResults"`
	TransactionId     string               `json:"transactionId"`
}

func GetGlobalMessages() ([]*Message, error) {
	messages := []*Message{}
	err := findAll(adapter.db, "message", &messages, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return messages, err
	}
	return messages, nil
}

func GetGlobalFailMessages() ([]*Message, error) {
	messages := []*Message{}
	err := findAll(adapter.db, "message", &messages, dbx.NewExp("error_text != ?", dbx.Params{"p0": ""}), "owner ASC", "created_time DESC")
	if err != nil {
		return messages, err
	}
	return messages, nil
}

func GetGlobalMessagesByStoreName(storeName string) ([]*Message, error) {
	messages := []*Message{}
	err := findAll(adapter.db, "message", &messages, dbx.HashExp{"store": storeName}, "owner ASC", "created_time ASC")
	if err != nil {
		return messages, err
	}
	return messages, nil
}

func GetChatMessages(chat string) ([]*Message, error) {
	messages := []*Message{}
	err := findAll(adapter.db, "message", &messages, dbx.HashExp{"chat": chat}, "created_time ASC")
	if err != nil {
		return messages, err
	}
	return messages, nil
}

func GetMessages(owner string, user string, storeName string) ([]*Message, error) {
	messages := []*Message{}
	err := findAll(adapter.db, "message", &messages, dbx.HashExp{"owner": owner, "user": user, "store": storeName}, "created_time DESC")
	if err != nil {
		return messages, err
	}
	return messages, nil
}

func GetNearMessageCount(user string, limitMinutes int) (int, error) {
	sinceTime := time.Now().Add(-time.Minute * time.Duration(limitMinutes))
	nearMessageCount, err := countWhere(adapter.db, "message", dbx.And(
		dbx.HashExp{"owner": "admin", "user": user, "author": "AI"},
		dbx.NewExp("created_time >= {:since}", dbx.Params{"since": sinceTime}),
	))
	if err != nil {
		return -1, err
	}
	return int(nearMessageCount), nil
}

func getMessage(owner, name string) (*Message, error) {
	message := Message{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "message", &message, pk2(message.Owner, message.Name))
	if err != nil {
		return &message, err
	}
	if existed {
		return &message, nil
	} else {
		return nil, nil
	}
}

func GetMessage(id string) (*Message, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getMessage(owner, name)
}

func UpdateMessage(id string, message *Message, isHitOnly bool) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	originMessage, err := getMessage(owner, name)
	if err != nil {
		return false, err
	}
	if message == nil {
		return false, nil
	}
	if originMessage.TextTokenCount == 0 || originMessage.Text != message.Text {
		size, err := getMessageTextTokenCount(message.ModelProvider, message.Text)
		if err != nil {
			return false, err
		}
		message.TextTokenCount = size
	}
	if isHitOnly {
		err = adapter.db.Model(message).Exclude().Update()
	} else {
		message.Owner = owner
		message.Name = name
		err = adapter.db.Model(message).Update()
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func RefineMessageFiles(message *Message, origin string, lang string) error {
	text := message.Text
	// re := regexp.MustCompile(`data:image\/([a-zA-Z]*);base64,([^"]*)`)
	re := regexp.MustCompile(`data:([a-zA-Z]*\/[a-zA-Z\-\.]*);base64,[a-zA-Z0-9+/=]+`)
	matches := re.FindAllString(text, -1)
	if matches != nil {
		store, err := GetDefaultStore("admin")
		if err != nil {
			return err
		}
		obj, err := store.GetImageProviderObj(lang)
		if err != nil {
			return err
		}
		for _, match := range matches {
			var content []byte
			content, err = parseBase64Image(match, lang)
			if err != nil {
				return err
			}
			filePath := fmt.Sprintf("%s/%s/%s/%s", message.Organization, message.User, message.Chat, message.FileName)
			var fileUrl string
			fileUrl, err = obj.PutObject(message.User, message.Chat, filePath, bytes.NewBuffer(content))
			if err != nil {
				return err
			}
			if strings.Contains(fileUrl, "?") {
				tokens := strings.Split(fileUrl, "?")
				fileUrl = tokens[0]
			}
			var httpUrl string
			httpUrl, err = getUrlFromPath(fileUrl, origin)
			if err != nil {
				return err
			}
			text = strings.Replace(text, match, httpUrl, 1)
		}
	}
	message.Text = text
	return nil
}

func AddMessage(message *Message) (bool, error) {
	size, err := getMessageTextTokenCount(message.ModelProvider, message.Text)
	if err != nil {
		return false, err
	}
	message.TextTokenCount = size
	err = insertRow(adapter.db, message)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	if affected != 0 {
		var chat *Chat
		chat, err = getChat(message.Owner, message.Chat)
		if err != nil {
			return false, err
		}
		if chat != nil {
			chat.UpdatedTime = util.GetCurrentTime()
			chat.MessageCount += 1
			_, err = UpdateChat(chat.GetId(), chat)
			if err != nil {
				return false, err
			}
		}
	}
	return affected != 0, nil
}

func DeleteMessage(message *Message) (bool, error) {
	affected, err := deleteByPK(adapter.db, "message", pk2(message.Owner, message.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteAllLaterMessages(messageId string) error {
	originMessage, err := GetMessage(messageId)
	if err != nil {
		return err
	}
	// Get all messages for this chat
	allMessages, err := GetChatMessages(originMessage.Chat)
	if err != nil {
		return err
	}
	// Find and delete messages created after the original message
	for _, msg := range allMessages {
		if msg.CreatedTime >= originMessage.CreatedTime {
			_, err := DeleteMessage(msg)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func DeleteMessagesByChat(message *Message) (bool, error) {
	affected, err := deleteWhere(adapter.db, "message", dbx.HashExp{"owner": message.Owner, "chat": message.Chat})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (message *Message) GetId() string {
	return fmt.Sprintf("%s/%s", message.Owner, message.Name)
}

func GetRecentRawMessages(chat string, createdTime string, memoryLimit int) ([]*model.RawMessage, error) {
	res := []*model.RawMessage{}
	if memoryLimit == 0 {
		return res, nil
	}
	messages := []*Message{}
	err := adapter.db.Select().From("message").
		Where(dbx.And(
			dbx.NewExp("created_time <= {:ct}", dbx.Params{"ct": createdTime}),
			dbx.HashExp{"chat": chat},
		)).
		OrderBy("created_time DESC").
		Offset(2).Limit(int64(2 * memoryLimit)).
		All(&messages)
	if err != nil {
		return nil, err
	}
	for _, message := range messages {
		rawTextTokenCount := message.TextTokenCount
		if rawTextTokenCount == 0 {
			rawTextTokenCount, err = getMessageTextTokenCount(message.ModelProvider, message.Text)
			if err != nil {
				return nil, err
			}
		}
		rawMessage := &model.RawMessage{
			Text:           message.Text,
			Author:         message.Author,
			TextTokenCount: message.TextTokenCount,
		}
		res = append(res, rawMessage)
	}
	return res, nil
}

type MyWriter struct {
	bytes.Buffer
}

func (w *MyWriter) Flush() {}
func (w *MyWriter) Write(p []byte) (n int, err error) {
	s := string(p)
	if strings.HasPrefix(s, "event: message\ndata: ") && strings.HasSuffix(s, "\n\n") {
		data := strings.TrimSuffix(strings.TrimPrefix(s, "event: message\ndata: "), "\n\n")
		return w.Buffer.WriteString(data)
	} else if strings.HasPrefix(s, "event: reason\ndata: ") && strings.HasSuffix(s, "\n\n") {
		return w.Buffer.WriteString("")
	}
	return w.Buffer.Write(p)
}

func GetAnswer(provider string, question string, lang string) (string, *model.ModelResult, error) {
	history := []*model.RawMessage{}
	knowledge := []*model.RawMessage{}
	return GetAnswerWithContext(provider, question, history, knowledge, "", lang)
}

func GetAnswerWithContext(provider string, question string, history []*model.RawMessage, knowledge []*model.RawMessage, prompt string, lang string) (string, *model.ModelResult, error) {
	_, modelProviderObj, err := GetModelProviderFromContext("admin", provider, lang)
	if err != nil {
		return "", nil, err
	}
	if prompt == "" {
		prompt = "You are an expert in your field and you specialize in using your knowledge to answer or solve people's problems."
	}
	var writer MyWriter
	modelResult, err := modelProviderObj.QueryText(question, &writer, history, prompt, knowledge, nil, lang)
	if err != nil {
		return "", nil, err
	}
	res := writer.String()
	res = strings.Trim(res, "\"")
	return res, modelResult, nil
}

func GetMessageCount(owner string, field string, value string, store string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	if store != "" {
		session = session.AndWhere(dbx.HashExp{"store": store})
	}
	return queryCount(session, "message")
}

func GetPaginationMessages(owner string, offset, limit int, field, value, sortField, sortOrder, store string) ([]*Message, error) {
	messages := []*Message{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	if store != "" {
		session = session.AndWhere(dbx.HashExp{"store": store})
	}
	err := queryFind(session, "message", &messages)
	if err != nil {
		return messages, err
	}
	return messages, nil
}

func getMessageTextTokenCount(modelName string, text string) (int, error) {
	tokenCount, err := model.GetTokenSize(modelName, text)
	if err != nil {
		tokenCount, err = model.GetTokenSize("gpt-3.5-turbo", text)
	}
	if err != nil {
		return 0, err
	}
	return tokenCount, nil
}
