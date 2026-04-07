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
	"fmt"

	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)

type Chat struct {
	Owner         string   `db:"pk" json:"owner"`
	Name          string   `db:"pk" json:"name"`
	CreatedTime   string   `json:"createdTime"`
	UpdatedTime   string   `json:"updatedTime"`
	Organization  string   `json:"organization"`
	DisplayName   string   `json:"displayName"`
	Store         string   `json:"store"`
	ModelProvider string   `json:"modelProvider"`
	Category      string   `json:"category"`
	Type          string   `json:"type"`
	User          string   `json:"user"`
	User1         string   `json:"user1"`
	User2         string   `json:"user2"`
	Users         []string `json:"users"`
	ClientIp      string   `json:"clientIp"`
	UserAgent     string   `json:"userAgent"`
	ClientIpDesc  string   `json:"clientIpDesc"`
	UserAgentDesc string   `json:"userAgentDesc"`
	MessageCount  int      `json:"messageCount"`
	TokenCount    int      `json:"tokenCount"`
	Price         float64  `json:"price"`
	Currency      string   `json:"currency"`
	IsHidden      bool     `json:"isHidden"`
	IsDeleted     bool     `json:"isDeleted"`
	NeedTitle     bool     `json:"needTitle"`
}

func GetGlobalChats() ([]*Chat, error) {
	chats := []*Chat{}
	err := findAll(adapter.db, "chat", &chats, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return chats, err
	}
	return chats, nil
}

func GetChats(owner string, storeName string, user string) ([]*Chat, error) {
	chats := []*Chat{}
	err := findAll(adapter.db, "chat", &chats, dbx.HashExp{"owner": owner, "user": user, "store": storeName}, "updated_time DESC")
	if err != nil {
		return chats, err
	}
	return chats, nil
}

func getChat(owner, name string) (*Chat, error) {
	chat := Chat{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "chat", &chat, pk2(chat.Owner, chat.Name))
	if err != nil {
		return nil, err
	}
	if existed {
		return &chat, nil
	} else {
		return nil, nil
	}
}

func GetChat(id string) (*Chat, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getChat(owner, name)
}

func UpdateChat(id string, chat *Chat) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	_, err = getChat(owner, name)
	if err != nil {
		return false, err
	}
	if chat == nil {
		return false, nil
	}
	chat.Owner = owner
	chat.Name = name
	err = adapter.db.Model(chat).Update()
	if err != nil {
		return false, err
	}
	// return affected != 0
	return true, nil
}

func AddChat(chat *Chat) (bool, error) {
	//if chat.Type == "AI" && chat.User2 == "" {
	//	provider, err := GetDefaultModelProvider()
	//	if err != nil {
	//		return false, err
	//	}
	//
	//	if provider != nil {
	//		chat.User2 = provider.Name
	//	}
	//}
	err := insertRow(adapter.db, chat)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteChat(chat *Chat) (bool, error) {
	affected, err := deleteByPK(adapter.db, "chat", pk2(chat.Owner, chat.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (chat *Chat) GetId() string {
	return fmt.Sprintf("%s/%s", chat.Owner, chat.Name)
}

func getChatCountByMessages(owner string, value string, store string) (int64, error) {
	q := adapter.db.Select("COUNT(DISTINCT chat.owner, chat.name)").From("chat").
		InnerJoin("message", dbx.NewExp("chat.owner = message.owner AND chat.name = message.chat")).
		Where(dbx.Like("message.text", value))
	if owner != "" {
		q = q.AndWhere(dbx.NewExp("chat.owner = {:owner}", dbx.Params{"owner": owner}))
	}
	if store != "" {
		q = q.AndWhere(dbx.NewExp("chat.store = {:store}", dbx.Params{"store": store}))
	}
	var count int64
	err := q.Row(&count)
	return count, err
}

func GetChatCount(owner string, field string, value string, store string) (int64, error) {
	if field == "messages" && value != "" {
		return getChatCountByMessages(owner, value, store)
	}
	q := GetDbQuery(owner, -1, -1, field, value, "", "")
	if store != "" {
		q = q.AndWhere(dbx.HashExp{"store": store})
	}
	return queryCount(q, "chat")
}

func getPaginationChatsByMessages(owner string, offset, limit int, value, sortField, sortOrder, store string) ([]*Chat, error) {
	chats := []*Chat{}
	q := adapter.db.Select("DISTINCT chat.*").From("chat").
		InnerJoin("message", dbx.NewExp("chat.owner = message.owner AND chat.name = message.chat")).
		Where(dbx.Like("message.text", value))
	if owner != "" {
		q = q.AndWhere(dbx.NewExp("chat.owner = {:owner}", dbx.Params{"owner": owner}))
	}
	if store != "" {
		q = q.AndWhere(dbx.NewExp("chat.store = {:store}", dbx.Params{"store": store}))
	}
	if sortField == "" || sortOrder == "" {
		sortField = "created_time"
	}
	col := fmt.Sprintf("chat.%s", util.SnakeString(sortField))
	if sortOrder == "ascend" {
		q = q.OrderBy(col + " ASC")
	} else {
		q = q.OrderBy(col + " DESC")
	}
	if offset != -1 && limit != -1 {
		q = q.Offset(int64(offset)).Limit(int64(limit))
	}
	err := q.All(&chats)
	if err != nil {
		return chats, err
	}
	return chats, nil
}

func GetPaginationChats(owner string, offset, limit int, field, value, sortField, sortOrder string, store string) ([]*Chat, error) {
	if field == "messages" && value != "" {
		return getPaginationChatsByMessages(owner, offset, limit, value, sortField, sortOrder, store)
	}
	chats := []*Chat{}
	q := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	if store != "" {
		q = q.AndWhere(dbx.HashExp{"store": store})
	}
	err := queryFind(q, "chat", &chats)
	if err != nil {
		return chats, err
	}
	return chats, nil
}
