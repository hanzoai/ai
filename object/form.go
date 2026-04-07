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

	"github.com/hanzoai/cloud/i18n"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)

type FormItem struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	Visible bool   `json:"visible"`
	Width   string `json:"width"`
}
type Form struct {
	Owner       string      `db:"pk" json:"owner"`
	Name        string      `db:"pk" json:"name"`
	CreatedTime string      `json:"createdTime"`
	DisplayName string      `json:"displayName"`
	Position    string      `json:"position"`
	Category    string      `json:"category"`
	Type        string      `json:"type"`
	Tag         string      `json:"tag"`
	Url         string      `json:"url"`
	FormItems   []*FormItem `json:"formItems"`
}

func GetMaskedForm(form *Form, isMaskEnabled bool) *Form {
	if !isMaskEnabled {
		return form
	}
	if form == nil {
		return nil
	}
	return form
}

func GetMaskedForms(forms []*Form, isMaskEnabled bool) []*Form {
	if !isMaskEnabled {
		return forms
	}
	for _, form := range forms {
		form = GetMaskedForm(form, isMaskEnabled)
	}
	return forms
}

func GetGlobalForms() ([]*Form, error) {
	forms := []*Form{}
	err := findAll(adapter.db, "form", &forms, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return forms, err
	}
	return forms, nil
}

func GetForms(owner string) ([]*Form, error) {
	forms := []*Form{}
	err := findAll(adapter.db, "form", &forms, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return forms, err
	}
	return forms, nil
}

func getForm(owner string, name string) (*Form, error) {
	form := Form{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "form", &form, pk2(form.Owner, form.Name))
	if err != nil {
		return &form, err
	}
	if existed {
		return &form, nil
	} else {
		return nil, nil
	}
}

func GetForm(id string) (*Form, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getForm(owner, name)
}

func UpdateForm(id string, form *Form, lang string) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	existingForm, err := getForm(owner, name)
	if existingForm == nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:the form: %s is not found"), id))
	}
	if err != nil {
		return false, err
	}
	if form == nil {
		return false, nil
	}
	form.Owner = owner
	form.Name = name
	err = adapter.db.Model(form).Update()
	if err != nil {
		return false, err
	}
	// return affected != 0
	return true, nil
}

func AddForm(form *Form) (bool, error) {
	err := insertRow(adapter.db, form)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteForm(form *Form) (bool, error) {
	affected, err := deleteByPK(adapter.db, "form", pk2(form.Owner, form.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (form *Form) GetId() string {
	return fmt.Sprintf("%s/%s", form.Owner, form.Name)
}

func GetFormCount(owner string, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "form")
}

func GetPaginationForms(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Form, error) {
	forms := []*Form{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "form", &forms)
	if err != nil {
		return forms, err
	}
	return forms, nil
}
