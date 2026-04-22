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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/beego/beego/context"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/i18n"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)

var logPostOnly bool

func init() {
	logPostOnly = conf.GetConfigBool("logPostOnly")
}

type Record struct {
	Id           int    `db:"pk" json:"id"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	CreatedTime  string `json:"createdTime"`
	Organization string `json:"organization"`
	ClientIp     string `json:"clientIp"`
	UserAgent    string `json:"userAgent"`
	User         string `json:"user"`
	Method       string `json:"method"`
	RequestUri   string `json:"requestUri"`
	Action       string `json:"action"`
	Language     string `json:"language"`
	Query        string `json:"query"`
	Region       string `json:"region"`
	City         string `json:"city"`
	Unit         string `json:"unit"`
	Section      string `json:"section"`
	Object       string `json:"object"`
	Response     string `json:"response"`
	ErrorText    string `json:"errorText"`
	// ExtendedUser *User  `db:"-" json:"extendedUser"`
	Provider     string `json:"provider"`
	Block        string `json:"block"`
	BlockHash    string `json:"blockHash"`
	Transaction  string `json:"transaction"`
	Provider2    string `json:"provider2"`
	Block2       string `json:"block2"`
	BlockHash2   string `json:"blockHash2"`
	Transaction2 string `json:"transaction2"`
	// For cross-chain records
	Count       int  `json:"count"`
	IsTriggered bool `json:"isTriggered"`
	NeedCommit  bool `db:"index" json:"needCommit"`
}
type Response struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
}

func GetRecordCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "record")
}

func GetRecords(owner string) ([]*Record, error) {
	records := []*Record{}
	err := findAll(adapter.db, "record", &records, dbx.HashExp{"owner": owner}, "id DESC")
	if err != nil {
		return records, err
	}
	return records, nil
}

func getAllRecords() ([]*Record, error) {
	records := []*Record{}
	err := findAll(adapter.db, "record", &records, dbx.HashExp{}, "id DESC")
	if err != nil {
		return records, err
	}
	return records, nil
}

func getValidAndNeedCommitRecords(records []*Record) ([]*Record, []int, []interface{}, error) {
	providerFirst, providerSecond, err := GetTwoActiveBlockchainProvider("admin")
	if err != nil {
		return nil, nil, nil, err
	}
	var validRecords []*Record
	var needCommitIdx []int
	var data []interface{}
	recordTime := util.GetCurrentTimeWithMilli()
	for i, record := range records {
		ok, err := prepareRecord(record, providerFirst, providerSecond)
		if err != nil {
			return nil, nil, nil, err
		}
		if !ok {
			continue
		}
		record.CreatedTime = util.GetCurrentTimeBasedOnLastMilli(recordTime)
		recordTime = record.CreatedTime
		validRecords = append(validRecords, record)
		data = append(data, map[string]interface{}{"name": record.Name})
		if record.NeedCommit {
			needCommitIdx = append(needCommitIdx, i)
		}
	}
	return validRecords, needCommitIdx, data, nil
}

func GetPaginationRecords(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Record, error) {
	records := []*Record{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "record", &records)
	if err != nil {
		return records, err
	}
	return records, nil
}

// GetRecord retrieves a record by its ID or owner/name format.
func GetRecord(id string, lang string) (*Record, error) {
	record := &Record{}
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to parse record identifier '%s': neither a valid owner/[id|name] format"), id))
	}
	// Try to parse as integer ID first
	if recordId, err := util.ParseIntWithError(name); err == nil && recordId > 0 {
		// Valid integer ID
		record.Id = recordId
	} else {
		record.Owner = owner
		record.Name = name
	}
	var existed bool
	if record.Id != 0 {
		existed, err = getOne(adapter.db, "record", record, pkID(record.Id))
	} else {
		existed, err = getOne(adapter.db, "record", record, pk2(record.Owner, record.Name))
	}
	if err != nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to get record with id '%s': %w"), id, err))
	}
	if existed {
		return record, nil
	}
	return nil, nil
}

func prepareRecord(record *Record, providerFirst, providerSecond *Provider) (bool, error) {
	if logPostOnly && record.Method == "GET" {
		return false, nil
	}
	if strings.HasSuffix(record.Action, "-record") {
		return false, nil
	}
	if strings.HasSuffix(record.Action, "-record-second") {
		return false, nil
	}
	if strings.HasSuffix(record.Action, "-records") {
		return false, nil
	}
	if record.Provider == "" {
		if providerFirst != nil {
			record.Provider = providerFirst.Name
		}
		if providerSecond != nil {
			record.Provider2 = providerSecond.Name
		}
	}
	record.Id = 0
	record.Name = util.GenerateId()
	record.Owner = record.Organization
	// Set default count to 1 if not set
	if record.Count == 0 {
		record.Count = 1
	}
	return true, nil
}

func UpdateRecord(id string, record *Record, lang string) (bool, error) {
	p, err := GetRecord(id, lang)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}
	// Update provider
	if record.Provider != p.Provider {
		record.Block = ""
		record.BlockHash = ""
		record.Transaction = ""
	}
	if record.Provider2 != p.Provider2 {
		record.Block2 = ""
		record.BlockHash2 = ""
		record.Transaction2 = ""
	}
	record.Id = int(p.Id)
	err = adapter.db.Model(record).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func UpdateRecordInternal(id int, record Record) error {
	err := adapter.db.Model(&record).Update()
	if err != nil {
		return err
	}
	return nil
}

func UpdateRecordFields(id string, fields map[string]interface{}, lang string) (bool, error) {
	p, err := GetRecord(id, lang)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}
	affected, err := updateByPK(adapter.db, "record", pkID(int(p.Id)), dbx.Params(fields))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func NewRecord(ctx *context.Context) (*Record, error) {
	ip := strings.Replace(util.GetIPFromRequest(ctx.Request), ": ", "", -1)
	action := strings.TrimPrefix(ctx.Request.URL.Path, "/v1/")
	requestUri := util.FilterQuery(ctx.Request.RequestURI, []string{"accessToken"})
	if len(requestUri) > 1000 {
		requestUri = requestUri[0:1000]
	}
	object := ""
	if len(ctx.Input.RequestBody) != 0 {
		object = string(ctx.Input.RequestBody)
	}
	respBytes, err := json.Marshal(ctx.Input.Data()["json"])
	if err != nil {
		return nil, err
	}
	var resp Response
	err = json.Unmarshal(respBytes, &resp)
	if err != nil {
		return nil, err
	}
	language := ctx.Request.Header.Get("Accept-Language")
	if len(language) > 2 {
		language = language[0:2]
	}
	languageCode := conf.GetLanguage(language)
	// get location info from client ip
	locationInfo, err := util.GetInfoFromIP(ip)
	if err != nil {
		return nil, err
	}
	region := locationInfo.Country
	city := locationInfo.City
	record := Record{
		Name:        util.GenerateId(),
		CreatedTime: util.GetCurrentTimeWithMilli(),
		ClientIp:    ip,
		User:        "",
		Method:      ctx.Request.Method,
		RequestUri:  requestUri,
		Action:      action,
		Language:    languageCode,
		Region:      region,
		City:        city,
		Object:      object,
		Response:    fmt.Sprintf("{\"status\":\"%s\",\"msg\":\"%s\"}", resp.Status, resp.Msg),
		Count:       1,
		IsTriggered: false,
	}
	return &record, nil
}

func AddRecord(record *Record, lang string) (bool, interface{}, error) {
	providerFirst, providerSecond, err := GetTwoActiveBlockchainProvider(record.Owner)
	if err != nil {
		return false, nil, err
	}
	ok, err := prepareRecord(record, providerFirst, providerSecond)
	if err != nil {
		return false, nil, err
	}
	if !ok {
		return false, nil, nil
	}
	record.CreatedTime = util.GetCurrentTimeWithMilli()
	// Set default count to 1 if not set
	if record.Count == 0 {
		record.Count = 1
	}
	err = insertRow(adapter.db, record)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, nil, err
	}
	data := map[string]interface{}{"name": record.Name}
	if record.NeedCommit {
		_, commitResult, err := CommitRecord(record, lang)
		if err != nil {
			data["error_text"] = err.Error()
		} else {
			data = commitResult
		}
	}
	return affected != 0, data, nil
}

func AddRecords(records []*Record, syncEnabled bool, lang string) (bool, interface{}, error) {
	if len(records) == 0 {
		return false, nil, nil
	}
	validRecords, needCommitRecordsIdx, data, err := getValidAndNeedCommitRecords(records)
	if err != nil {
		return false, nil, err
	}
	if len(validRecords) == 0 {
		return false, nil, nil
	}
	totalAffected := int64(0)
	err = adapter.db.Transactional(func(tx *dbx.Tx) error {
		batchSize := 150
		for i := 0; i < len(validRecords); i += batchSize {
			end := min(i+batchSize, len(validRecords))
			batch := validRecords[i:end]
			for _, r := range batch {
				if err := tx.Model(r).Insert(); err != nil {
					return err
				}
				totalAffected++
			}
		}
		return nil
	})
	if err != nil {
		return false, nil, err
	}
	// Send commit event for records that need to be committed
	if len(needCommitRecordsIdx) > 0 {
		if syncEnabled {
			var needCommitRecords []*Record
			for _, idx := range needCommitRecordsIdx {
				needCommitRecords = append(needCommitRecords, records[idx])
			}
			_, commitResults := CommitRecords(needCommitRecords, lang)
			for i, idx := range needCommitRecordsIdx {
				data[idx] = commitResults[i]
			}
		} else {
			go ScanNeedCommitRecords()
		}
	}
	return totalAffected != 0, data, nil
}

func DeleteRecord(record *Record) (bool, error) {
	affected, err := deleteByPK(adapter.db, "record", pkID(int(record.Id)))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (record *Record) getUniqueId() string {
	return fmt.Sprintf("%s/%d", record.Owner, record.Id)
}

func (record *Record) getId() string {
	return fmt.Sprintf("%s/%s", record.Owner, record.Name)
}

func (r *Record) updateErrorText(errText string, lang string) (bool, error) {
	r.ErrorText = errText
	if r.Id != 0 {
		affected, err := updateCols(adapter.db, "record", pk2(r.Owner, r.Name), dbx.Params{"error_text": r.ErrorText})
		if err != nil {
			return affected > 0, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to update error text for record %s: %s"), r.getId(), err))
		}
		return affected > 0, nil
	} else {
		affected, err := updateCols(adapter.db, "record", pkID(r.Id), dbx.Params{"error_text": r.ErrorText})
		if err != nil {
			return affected > 0, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to update error text for record %s: %s"), r.getUniqueId(), err))
		}
		return affected > 0, nil
	}
}
