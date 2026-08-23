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
	"bytes"
	"text/template"

	"github.com/hanzoai/ai/util"
)

type Template struct {
	Owner              string                 `db:"pk" json:"owner"`
	Name               string                 `db:"pk" json:"name"`
	CreatedTime        string                 `json:"createdTime"`
	UpdatedTime        string                 `json:"updatedTime"`
	DisplayName        string                 `json:"displayName"`
	Description        string                 `json:"description"`
	Version            string                 `json:"version"`
	Icon               string                 `json:"icon"`
	Manifest           string                 `json:"manifest"`
	Readme             string                 `json:"readme"`
	EnableBasicConfig  bool                   `json:"enableBasicConfig"`
	BasicConfigOptions []templateConfigOption `db:"json" json:"basicConfigOptions"`
}
type templateConfigOption struct {
	Parameter   string     `json:"parameter" yaml:"parameter"`
	Description string     `json:"description" yaml:"description"`
	Type        string     `json:"type" yaml:"type"` // string, number, boolean, option
	Options     StringList `json:"options" yaml:"options"`
	Default     string     `json:"default" yaml:"default"`
	Required    bool       `json:"required" yaml:"required"`
}

func GetTemplates(owner string) ([]*Template, error) {
	return rowsOf[Template]("template", owner)
}

func GetTemplateCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "template")
}

func GetPaginationTemplates(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Template, error) {
	templates := []*Template{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "template", &templates)
	if err != nil {
		return templates, err
	}
	return templates, nil
}

func GetTemplate(id string) (*Template, error) {
	return rowAt[Template]("template", id)
}

func getTemplate(owner, name string) (*Template, error) {
	return getRow[Template]("template", owner, name)
}

func UpdateTemplate(id string, template *Template) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	template.UpdatedTime = util.GetCurrentTime()
	_, err = getTemplate(owner, name)
	if err != nil {
		return false, err
	}
	if template == nil {
		return false, nil
	}
	template.Owner = owner
	template.Name = name
	return updated(template)
}

func AddTemplate(template *Template) (bool, error) {
	if template.CreatedTime == "" {
		template.CreatedTime = util.GetCurrentTime()
	}
	if template.UpdatedTime == "" {
		template.UpdatedTime = util.GetCurrentTime()
	}
	return addRow(template)
}

func DeleteTemplate(template *Template) (bool, error) {
	return deleteRow("template", template.Owner, template.Name)
}

// Render the template with the given data.
func (t *Template) Render(data map[string]interface{}) (string, error) {
	if data == nil {
		data = map[string]interface{}{}
	}
	textTmpl := template.New("manifest")
	tpl, err := textTmpl.Parse(t.Manifest)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
