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

//go:build !skipCi

package object

import (
	"fmt"
	"sort"
	"testing"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/util"
)

func TestUpdateChatCounts(t *testing.T) {
	requireStore(t)
	InitConfig()
	chats, err := GetGlobalChats()
	if err != nil {
		panic(err)
	}
	chatMap := map[string]*Chat{}
	for _, chat := range chats {
		chat.MessageCount = 0
		chatMap[chat.Name] = chat
	}
	messages, err := GetGlobalMessages()
	if err != nil {
		panic(err)
	}
	for _, message := range messages {
		chat, ok := chatMap[message.Chat]
		if ok {
			chat.MessageCount += 1
		}
	}
	for _, chat := range chats {
		_, err = UpdateChat(chat.GetId(), chat)
		if err != nil {
			panic(err)
		}
	}
}

func TestUpdateChatPrices(t *testing.T) {
	requireStore(t)
	InitConfig()
	chats, err := GetGlobalChats()
	if err != nil {
		panic(err)
	}
	chatMap := map[string]*Chat{}
	for _, chat := range chats {
		chat.TokenCount = 0
		chat.Price = 0
		chat.Currency = "USD"
		chatMap[chat.Name] = chat
	}
	messages, err := GetGlobalMessages()
	if err != nil {
		panic(err)
	}
	for _, message := range messages {
		chat, ok := chatMap[message.Chat]
		if ok {
			chat.TokenCount += message.TokenCount
			chat.Price = model.AddPrices(chat.Price, message.Price)
		}
	}
	for _, chat := range chats {
		_, err = UpdateChat(chat.GetId(), chat)
		if err != nil {
			panic(err)
		}
	}
}

func TestDeleteEmptyChats(t *testing.T) {
	requireStore(t)
	InitConfig()
	chats, err := GetGlobalChats()
	if err != nil {
		panic(err)
	}
	for i, chat := range chats {
		if chat.MessageCount == 0 {
			_, err = DeleteChat(chat)
			if err != nil {
				panic(err)
			}
			fmt.Printf("[%d/%d] deleted chat: %s, user: %s, clientIP: %s (%s), userAgent: %s (%s)\n", i+1, len(chats), chat.Name, chat.User, chat.ClientIpDesc, chat.ClientIp, chat.UserAgentDesc, chat.UserAgent)
		}
	}
}

func TestUpdateChatDescs(t *testing.T) {
	requireStore(t)
	InitConfig()
	util.InitIpDb()
	chats, err := GetGlobalChats()
	if err != nil {
		panic(err)
	}
	for _, chat := range chats {
		if chat.ClientIpDesc != "" && chat.UserAgentDesc != "" {
			continue
		}
		if chat.ClientIp == "" && chat.UserAgent == "" {
			continue
		}
		chat.ClientIpDesc = util.GetDescFromIP(chat.ClientIp)
		chat.UserAgentDesc = util.GetDescFromUserAgent(chat.UserAgent)
		_, err = UpdateChat(chat.GetId(), chat)
		if err != nil {
			panic(err)
		}
	}
}

func TestPrintChatUsers(t *testing.T) {
	requireStore(t)
	InitConfig()
	chats, err := GetGlobalChats()
	if err != nil {
		panic(err)
	}
	userMap := make(map[string]struct{})
	users := []string{}
	for _, chat := range chats {
		if chat.User == "admin" {
			continue
		}
		if _, exists := userMap[chat.User]; !exists {
			userMap[chat.User] = struct{}{}
			users = append(users, chat.User)
		}
	}
	sort.Strings(users)
	for _, user := range users {
		fmt.Printf("%s\n", user)
	}
	fmt.Printf("\nCount: %d\n\n", len(users))
	var concatenatedUsers string
	for i, user := range users {
		if i > 0 {
			concatenatedUsers += " or "
		}
		concatenatedUsers += fmt.Sprintf(`name = "%s"`, user)
	}
	fmt.Println(concatenatedUsers)
}

// A store that was never initialised answers an error, and does not crash.
//
// The adapter is nil before the DB is up — during boot, in the standalone runtime
// with no driverName, and in every unit test — and this read is on the anonymous
// sign-in path, so an unguarded one landed as a panic on GET /v1/ai/account and a
// 500 for a caller who had asked for nothing unusual. The caller returns this
// error rather than carrying on, so the sequence stops here.
func TestChatsWithoutAStoreAnswerAnErrorRatherThanCrash(t *testing.T) {
	saved := adapter
	adapter = nil
	t.Cleanup(func() { adapter = saved })

	chats, err := GetChats("admin", "", "someone")
	if err == nil {
		t.Fatal("GetChats answered no error with no store behind it")
	}
	if chats != nil {
		t.Errorf("chats=%v, want none", chats)
	}
}
