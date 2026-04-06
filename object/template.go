// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
package object
import (
	"bytes"
	"text/template"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)
type Template struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Icon        string `json:"icon"`
	Manifest    string `json:"manifest"`
	Readme      string `json:"readme"`
	EnableBasicConfig  bool                   `json:"enableBasicConfig"`
	BasicConfigOptions []templateConfigOption `db:"json" json:"basicConfigOptions"`
}
type templateConfigOption struct {
	Parameter   string   `json:"parameter" yaml:"parameter"`
	Description string   `json:"description" yaml:"description"`
	Type        string   `json:"type" yaml:"type"` // string, number, boolean, option
	Options     []string `json:"options" yaml:"options"`
	Default     string   `json:"default" yaml:"default"`
	Required    bool     `json:"required" yaml:"required"`
}
func GetTemplates(owner string) ([]*Template, error) {
	templates := []*Template{}
	err := findAll(adapter.db, "template", &templates, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return templates, err
	}
	return templates, nil
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
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getTemplate(owner, name)
}
func getTemplate(owner, name string) (*Template, error) {
	template := Template{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "template", &template, pk2(template.Owner, template.Name))
	if err != nil {
		return &template, err
	}
	if existed {
		return &template, nil
	} else {
		return nil, nil
	}
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
	err = adapter.db.Model(template).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func AddTemplate(template *Template) (bool, error) {
	if template.CreatedTime == "" {
		template.CreatedTime = util.GetCurrentTime()
	}
	if template.UpdatedTime == "" {
		template.UpdatedTime = util.GetCurrentTime()
	}
	err := insertRow(adapter.db, template)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteTemplate(template *Template) (bool, error) {
	affected, err := deleteByPK(adapter.db, "template", pk2(template.Owner, template.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
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
