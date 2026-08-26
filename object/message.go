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
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/util"
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
	Owner             string                       `db:"pk" json:"owner"`
	Name              string                       `db:"pk" json:"name"`
	CreatedTime       string                       `json:"createdTime"`
	Organization      string                       `json:"organization"`
	Store             string                       `json:"store"`
	User              string                       `json:"user"`
	Chat              string                       `json:"chat"`
	ReplyTo           string                       `json:"replyTo"`
	Author            string                       `json:"author"`
	Text              string                       `json:"text"`
	ReasonText        string                       `json:"reasonText"`
	ErrorText         string                       `json:"errorText"`
	FileName          string                       `json:"fileName"`
	Comment           string                       `json:"comment"`
	TokenCount        int                          `json:"tokenCount"`
	TextTokenCount    int                          `json:"textTokenCount"`
	Price             float64                      `json:"price"`
	Currency          string                       `json:"currency"`
	IsHidden          bool                         `json:"isHidden"`
	IsDeleted         bool                         `json:"isDeleted"`
	NeedNotify        bool                         `json:"needNotify"`
	IsAlerted         bool                         `json:"isAlerted"`
	IsRegenerated     bool                         `json:"isRegenerated"`
	WebSearchEnabled  bool                         `json:"webSearchEnabled"`
	ModelProvider     string                       `json:"modelProvider"`
	EmbeddingProvider string                       `json:"embeddingProvider"`
	VectorScores      JSONList[VectorScore]        `json:"vectorScores"`
	LikeUsers         StringList                   `json:"likeUsers"`
	DisLikeUsers      StringList                   `json:"dislikeUsers"`
	Suggestions       JSONList[Suggestion]         `json:"suggestions"`
	ToolCalls         JSONList[model.ToolCall]     `json:"toolCalls"`
	SearchResults     JSONList[model.SearchResult] `json:"searchResults"`
	TransactionId     string                       `json:"transactionId"`
	// ClaimedTime is when some request took the right to generate this answer, and
	// it is empty on a message nobody is answering. See ClaimMessageAnswer.
	ClaimedTime string `json:"claimedTime"`
	// AnsweredTime is when a generation TERMINATED on this message, and it is empty
	// on a message no generation has finished. See SettleMessageAnswer.
	AnsweredTime string `json:"answeredTime"`
}

// answerLease bounds how long ONE claim on a message's answer is honored. It exists
// for the generator that dies without releasing: past the lease the answer is
// claimable again, so a crash costs a delay rather than a message nobody can ever
// answer. It has to outlast a slow answer — reason models and agent flows stream for
// minutes — because a lease that expires mid-generation lets a second request in and
// that is the whole thing this prevents.
const answerLease = 15 * time.Minute

// ClaimMessageAnswer takes the exclusive right to generate this message's answer and
// reports whether this caller got it.
//
// It is ONE conditional UPDATE, so the database decides the winner. The condition —
// the answer is not written yet — is tested and acted on in a single statement,
// because between a read and a write two concurrent requests both see an
// unanswered message and both generate it.
//
// This is the module's ONLY exactly-once guarantee for an answer, and the debit rides
// on it: the ledger has none. It mints its own entry id per debit and reads no key we
// send (see AddTransactionForMessage), so two generations of one message are two
// completions AND two charges — an SSE reconnect was enough.
//
// A NULL claim is treated as unclaimed. Rows that predate the column hold SQL NULL
// until the boot repair reaches them (backfillNullField), and a row nobody can claim
// is a message nobody can answer.
//
// "Unanswered" is two facts, not one. An empty text column alone is what an EMPTY
// completion leaves behind — a tool-call-only turn, a filtered response, a carrier
// parse that strips everything — so on its own it would hand that message to the next
// request to generate AND charge again, forever. answered_time is the other half: it says a
// generation terminated here, whatever it produced. Both are required because they
// are independently true — an ordinary UpdateMessage writes text without ever running
// a generation — and requiring both can only ever refuse a claim, never grant one.
func ClaimMessageAnswer(message *Message) (bool, error) {
	now := util.GetCurrentTime()
	affected, err := updateCols(adapter.db, "message",
		dbx.And(
			pk2(message.Owner, message.Name),
			dbx.NewExp("text = {:unanswered}", dbx.Params{"unanswered": ""}),
			dbx.Or(
				dbx.NewExp("answered_time IS NULL"),
				dbx.NewExp("answered_time = {:unsettled}", dbx.Params{"unsettled": ""}),
			),
			dbx.Or(
				dbx.NewExp("claimed_time IS NULL"),
				dbx.NewExp("claimed_time = {:unclaimed}", dbx.Params{"unclaimed": ""}),
				dbx.NewExp("claimed_time < {:stale}", dbx.Params{"stale": util.GetTimeAgo(answerLease)}),
			),
		),
		dbx.Params{"claimed_time": now},
	)
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	message.ClaimedTime = now
	return true, nil
}

// SettleMessageAnswer records that a generation TERMINATED on this message, so no
// later request generates it again — whether or not it produced any text.
//
// The claim cannot say this by itself. It is held for answerLease and then abandoned,
// which is right for a generator that died mid-answer and wrong for one that finished
// empty: the row's text is still empty, so the next request wins the claim, runs the
// model again and takes a second debit. The debit is not idempotent — the ledger mints
// its own entry per call (AddTransactionForMessage) — so every repeat is another
// invoice for one turn.
//
// It is its own write, taken BEFORE the charge rather than folded into the row the
// handler persists afterwards, because a persist that failed after the charge landed
// would leave the message unanswered and chargeable again. Settling also drops the
// claim: the generation is over, so the row should not read as in flight.
func SettleMessageAnswer(message *Message) error {
	now := util.GetCurrentTime()
	_, err := updateCols(adapter.db, "message",
		pk2(message.Owner, message.Name),
		dbx.Params{"answered_time": now, "claimed_time": ""},
	)
	if err != nil {
		return err
	}
	message.AnsweredTime = now
	message.ClaimedTime = ""
	return nil
}

// ReleaseMessageAnswer drops a claim that produced no answer, so a generation that
// failed is retryable at once instead of at the end of the lease. Safe to call on
// every exit path: it is conditional on the answer still being empty, so it cannot
// disturb a generation that landed. The lease, not this call, is the guarantee — a
// process that dies never reaches it.
func ReleaseMessageAnswer(message *Message) {
	_, err := updateCols(adapter.db, "message",
		dbx.And(
			pk2(message.Owner, message.Name),
			dbx.NewExp("text = {:unanswered}", dbx.Params{"unanswered": ""}),
		),
		dbx.Params{"claimed_time": ""},
	)
	if err != nil {
		log.Warning("release answer claim %s: %v", message.GetId(), err)
	}
}

func GetGlobalMessages() ([]*Message, error) {
	return allRows[Message]("message")
}

func GetGlobalFailMessages() ([]*Message, error) {
	messages := []*Message{}
	err := findAll(adapter.db, "message", &messages, dbx.NewExp("error_text != {:p0}", dbx.Params{"p0": ""}), "owner ASC", "created_time DESC")
	if err != nil {
		return messages, err
	}
	return messages, nil
}

// GetGlobalMessagesByStoreName returns a store's messages oldest first.
//
// The order is the contract, not a detail: GetUsages walks these once, moving a
// day counter forward and never back, so a message out of time order is counted
// on the wrong day and every day after it. Sorting by owner first put them out of
// order the moment a store held messages from more than one — which it does, since
// a spoken answer is stored under its provider's owner while a chat's is stored
// under the namespace every chat shares.
// The organization narrows it because a store's NAME is what a message carries,
// and a name belongs to whoever chose it: two organizations may each keep a store
// called "docs", and a report that asks by name alone counts both as one. An empty
// organization is the reserved org, which reads them all; an empty store name is
// every store within it.
func GetGlobalMessagesByStoreName(org string, storeName string) ([]*Message, error) {
	messages := []*Message{}
	err := findAll(adapter.db, "message", &messages,
		narrow(dbx.HashExp{}, map[string]string{"organization": org, "store": storeName}),
		"created_time ASC")
	if err != nil {
		return messages, err
	}
	return messages, nil
}

// GetChatMessages returns one chat's transcript, confined to org. A chat name is
// unique across the whole store rather than within a tenant, so the name alone
// addresses any customer's conversation; org is what makes the answer the
// caller's. Empty asks across every tenant, which is the reserved org's to ask.
func GetChatMessages(chat string, org string) ([]*Message, error) {
	messages := []*Message{}
	err := findAll(adapter.db, "message", &messages,
		narrow(dbx.HashExp{"chat": chat}, map[string]string{"organization": org}),
		"created_time ASC")
	if err != nil {
		return messages, err
	}
	return messages, nil
}

// GetMessages lists messages, narrowed by whichever of org, user and store the
// caller named; an empty one means unconstrained. As with a chat, Owner is the
// namespace every message shares and org is the tenant.
func GetMessages(owner string, org string, user string, storeName string) ([]*Message, error) {
	messages := []*Message{}
	err := findAll(adapter.db, "message", &messages,
		narrow(dbx.HashExp{"owner": owner}, map[string]string{
			"organization": org, "user": user, "store": storeName,
		}), "created_time DESC")
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
	return getRow[Message]("message", owner, name)
}

func GetMessage(id string) (*Message, error) {
	return rowAt[Message]("message", id)
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
	// getMessage answers (nil, nil) for an id no row matches, so a miss arrives here as
	// a nil origin and not as an error — and reading TextTokenCount off it panics the
	// process. There is nothing to update, which is what false says.
	if originMessage == nil || message == nil {
		return false, nil
	}
	if originMessage.TextTokenCount == 0 || originMessage.Text != message.Text {
		size, err := getMessageTextTokenCount(message.ModelProvider, message.Text)
		if err != nil {
			return false, err
		}
		message.TextTokenCount = size
	}
	// The id is what was authorized; the body is only what to write. Both branches
	// key on it. The xorm form this replaced did — ID(core.PK{owner, name}) on each
	// — and without it an update names its own target: a body carrying another
	// message's name writes that message, and a body carrying no name writes
	// nothing while reporting that it wrote.
	message.Owner = owner
	message.Name = name
	if isHitOnly {
		// A hit changes the suggestions and nothing else, which is what the same
		// branch said as Cols("suggestions"). Exclude() with no arguments excludes
		// nothing, so it had come to write the whole row.
		err = adapter.db.Model(message).Update("Suggestions")
	} else {
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
		// GetDefaultStore answers (nil, nil) when there is no store to be the default
		// one — a fresh deployment, before anything is configured — so absence arrives
		// as a nil value beside a nil error, and the read below is on a field.
		if store == nil {
			return fmt.Errorf("%s", i18n.Translate(lang, "object:There is no default store"))
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
	if err != nil {
		return false, err
	}
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
	return true, nil
}

func DeleteMessage(message *Message) (bool, error) {
	return deleteRow("message", message.Owner, message.Name)
}

func DeleteAllLaterMessages(messageId string) error {
	originMessage, err := GetMessage(messageId)
	if err != nil {
		return err
	}
	// Nothing is later than a message that is not there.
	if originMessage == nil {
		return nil
	}
	// Get all messages for this chat
	allMessages, err := GetChatMessages(originMessage.Chat, originMessage.Organization)
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
			Text:   message.Text,
			Author: message.Author,
			// The count just computed, not the stored one it was computed because of.
			// Reading the stored zero back made the recompute above dead code and told
			// the history trimmer that an untallied message costs nothing, so a window
			// meant to be bounded carried whatever those messages happened to be.
			TextTokenCount: rawTextTokenCount,
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

// AnswerPrompt is the system prompt an answer runs under when its caller names
// none. It is a value rather than a literal inside the query so a caller that must
// PRICE an answer before making it estimates against the same prompt the answer
// will actually carry.
const AnswerPrompt = "You are an expert in your field and you specialize in using your knowledge to answer or solve people's problems."

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
	return QueryAnswer(modelProviderObj, question, history, knowledge, prompt, lang)
}

// QueryAnswer runs ONE completion against an ALREADY-RESOLVED provider. Resolving a
// provider by name and running a completion on it are two things, and a caller that
// gates on price needs the first before it may do the second: it resolves once, dry
// runs to estimate, and — if the payer can cover it — answers on that same provider.
// Braided together (as GetAnswerWithContext alone) that caller has to resolve twice.
func QueryAnswer(modelProviderObj model.ModelProvider, question string, history []*model.RawMessage, knowledge []*model.RawMessage, prompt string, lang string) (string, *model.ModelResult, error) {
	if prompt == "" {
		prompt = AnswerPrompt
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
