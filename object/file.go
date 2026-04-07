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
	"strings"

	"github.com/hanzoai/cloud/i18n"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)

type FileStatus string

const (
	FileStatusPending    FileStatus = "Pending"
	FileStatusProcessing FileStatus = "Processing"
	FileStatusFinished   FileStatus = "Finished"
	FileStatusError      FileStatus = "Error"
)

type File struct {
	Owner           string     `db:"pk" json:"owner"`
	Name            string     `db:"pk" json:"name"`
	CreatedTime     string     `json:"createdTime"`
	Filename        string     `json:"filename"`
	Size            int64      `json:"size"`
	Store           string     `json:"store"`
	StorageProvider string     `json:"storageProvider"`
	Url             string     `json:"url"`
	TokenCount      int        `json:"tokenCount"`
	Status          FileStatus `json:"status"`
	ErrorText       string     `json:"errorText"`
}

func GetGlobalFiles() ([]*File, error) {
	files := []*File{}
	err := findAll(adapter.db, "file", &files, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return files, err
	}
	return files, nil
}

func GetFiles(owner string) ([]*File, error) {
	files := []*File{}
	err := findAll(adapter.db, "file", &files, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return files, err
	}
	return files, nil
}

func GetFilesByStore(owner string, store string) ([]*File, error) {
	files := []*File{}
	err := findAll(adapter.db, "file", &files, dbx.HashExp{"owner": owner, "store": store}, "created_time DESC")
	if err != nil {
		return files, err
	}
	return files, nil
}

func getFile(owner string, name string) (*File, error) {
	file := File{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "file", &file, pk2(file.Owner, file.Name))
	if err != nil {
		return &file, err
	}
	if existed {
		return &file, nil
	} else {
		return nil, nil
	}
}

func GetFile(id string) (*File, error) {
	owner, name := util.GetOwnerAndNameFromIdNoCheck(id)
	return getFile(owner, name)
}

func UpdateFile(id string, file *File) (bool, error) {
	owner, name := util.GetOwnerAndNameFromIdNoCheck(id)
	_, err := getFile(owner, name)
	if err != nil {
		return false, err
	}
	if file == nil {
		return false, nil
	}
	file.Owner = owner
	file.Name = name
	err = adapter.db.Model(file).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}

func AddFile(file *File) (bool, error) {
	err := insertRow(adapter.db, file)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteFile(file *File, lang string) (bool, error) {
	var objectKey string
	prefix := fmt.Sprintf("%s_", file.Store)
	if strings.HasPrefix(file.Name, prefix) {
		objectKey = strings.TrimPrefix(file.Name, prefix)
	}
	if objectKey == "" {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The file: %s is not found"), file.Name))
	}
	store, err := getStore(file.Owner, file.Store)
	if err != nil {
		return false, err
	}
	if store == nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "account:The store: %s is not found"), file.Store))
	}
	storageProviderObj, err := store.GetStorageProviderObj(lang)
	if err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:The provider: %s does not exist"), store.StorageProvider))
	}
	err = storageProviderObj.DeleteObject(objectKey)
	if err != nil {
		return false, err
	}
	_, err = DeleteVectorsByFile(file.Owner, file.Store, objectKey)
	if err != nil {
		return false, err
	}
	affected, err := deleteByPK(adapter.db, "file", pk2(file.Owner, file.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (file *File) GetId() string {
	return fmt.Sprintf("%s/%s", file.Owner, file.Name)
}

func getFileName(storeName string, objectKey string) string {
	return fmt.Sprintf("%s_%s", storeName, objectKey)
}

func GetFileCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "file")
}

func GetPaginationFiles(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*File, error) {
	files := []*File{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "file", &files)
	if err != nil {
		return files, err
	}
	return files, nil
}

func updateFileStatus(owner string, storeName string, objectKey string, status FileStatus, errorText string, tokenCount int) error {
	name := getFileName(storeName, objectKey)
	params := dbx.Params{"status": string(status), "error_text": errorText}
	if status == FileStatusProcessing {
		params["token_count"] = 0
	} else if status == FileStatusFinished || status == FileStatusError {
		params["token_count"] = tokenCount
	}
	_, err := updateByPK(adapter.db, "file", pk2(owner, name), params)
	return err
}

func UpdateFilesStatusByStore(owner string, storeName string, status FileStatus) error {
	_, err := updateCols(adapter.db, "file", dbx.NewExp("owner = {:p0} AND store = {:p1}", dbx.Params{"p0": owner, "p1": storeName}), dbx.Params{"status": string(status), "error_text": ""})
	return err
}

func deleteFileRecord(owner string, storeName string, objectKey string) error {
	name := getFileName(storeName, objectKey)
	_, err := deleteByPK(adapter.db, "file", pk2(owner, name))
	return err
}
