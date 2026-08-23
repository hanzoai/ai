// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/util"
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
	return allRows[Form]("form")
}

func GetForms(owner string) ([]*Form, error) {
	return rowsOf[Form]("form", owner)
}

func getForm(owner, name string) (*Form, error) {
	return getRow[Form]("form", owner, name)
}

func GetForm(id string) (*Form, error) {
	return rowAt[Form]("form", id)
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
	return addRow(form)
}

func DeleteForm(form *Form) (bool, error) {
	return deleteRow("form", form.Owner, form.Name)
}

func (form *Form) GetId() string {
	return fmt.Sprintf("%s/%s", form.Owner, form.Name)
}

func GetFormCount(owner string, field, value string) (int64, error) {
	return rowCount("form", owner, field, value)
}

func GetPaginationForms(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Form, error) {
	return rowsPage[Form]("form", owner, offset, limit, field, value, sortField, sortOrder)
}
