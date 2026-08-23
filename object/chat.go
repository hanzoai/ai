// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
)

type Chat struct {
	Owner         string     `db:"pk" json:"owner"`
	Name          string     `db:"pk" json:"name"`
	CreatedTime   string     `json:"createdTime"`
	UpdatedTime   string     `json:"updatedTime"`
	Organization  string     `json:"organization"`
	DisplayName   string     `json:"displayName"`
	Store         string     `json:"store"`
	ModelProvider string     `json:"modelProvider"`
	Category      string     `json:"category"`
	Type          string     `json:"type"`
	User          string     `json:"user"`
	User1         string     `json:"user1"`
	User2         string     `json:"user2"`
	Users         StringList `json:"users"`
	ClientIp      string     `json:"clientIp"`
	UserAgent     string     `json:"userAgent"`
	ClientIpDesc  string     `json:"clientIpDesc"`
	UserAgentDesc string     `json:"userAgentDesc"`
	MessageCount  int        `json:"messageCount"`
	TokenCount    int        `json:"tokenCount"`
	Price         float64    `json:"price"`
	Currency      string     `json:"currency"`
	IsHidden      bool       `json:"isHidden"`
	IsDeleted     bool       `json:"isDeleted"`
	NeedTitle     bool       `json:"needTitle"`
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
	// The adapter is nil before the DB is initialised — during boot, in the
	// standalone runtime with no driverName, and in every unit test — so reading
	// through it turns a missing dependency into a SIGSEGV at whatever call site
	// asked first. This one is reached from anonymous sign-in, so the crash landed
	// on GET /v1/ai/account. The caller returns this error rather than carrying on,
	// which stops the sequence here instead of at whichever read came next.
	if adapter == nil || adapter.db == nil {
		return nil, fmt.Errorf("chat store is not initialised")
	}
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
	if chat == nil {
		return false, nil
	}
	chat.Owner, chat.Name = owner, name
	return updated(chat)
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
	return deleteRow("chat", chat.Owner, chat.Name)
}

func (chat *Chat) GetId() string {
	return fmt.Sprintf("%s/%s", chat.Owner, chat.Name)
}

// chatsMatchingMessages selects the chats holding at least one message whose
// text matches, and is the one predicate both the count and the page are taken
// from. EXISTS rather than a join because a join multiplies a chat by each of
// its matching messages and then every caller has to undo that: the page did it
// with DISTINCT and the count with COUNT(DISTINCT chat.owner, chat.name), a
// two-column form only MySQL accepts. With EXISTS a chat appears once by
// construction, so neither caller de-duplicates and the SQL is the same on every
// driver. The pattern is bound, and "%"+value+"%" is what dbx.Like builds — it
// escapes nothing, so this matches byte for byte.
func chatsMatchingMessages(owner, value, store string) *dbx.SelectQuery {
	q := adapter.db.Select("chat.*").From("chat").Where(dbx.Exists(dbx.NewExp(
		"SELECT 1 FROM message WHERE message.owner = chat.owner AND message.chat = chat.name AND message.text LIKE {:text}",
		dbx.Params{"text": "%" + value + "%"})))
	if owner != "" {
		q = q.AndWhere(dbx.NewExp("chat.owner = {:owner}", dbx.Params{"owner": owner}))
	}
	if store != "" {
		q = q.AndWhere(dbx.NewExp("chat.store = {:store}", dbx.Params{"store": store}))
	}
	return q
}

func getChatCountByMessages(owner string, value string, store string) (int64, error) {
	var count int64
	err := chatsMatchingMessages(owner, value, store).Select("COUNT(*)").Row(&count)
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
	// Same whitelist as GetDbQuery — searching by message text changes which
	// chats come back, not who supplies the sort column. The "chat." prefix is
	// not itself a guard: a comma opens a second ORDER BY term.
	q := chatsMatchingMessages(owner, value, store).
		OrderBy("chat." + sortColumn(sortField, sortOrder) + sortDirection(sortOrder))
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
