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
	"fmt"
	"time"

	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
)

// Prompt is an org-scoped, reusable prompt template — the backing store for the
// console's Prompts surface. It is owned by a tenant org (Owner) exactly like
// Store, so the same GetScopedOwner isolation applies: a tenant sees only its
// own prompts; the "admin" org owns the global defaults.
type Prompt struct {
	Owner       string      `db:"pk" json:"owner"`
	Name        string      `db:"pk" json:"name"`
	CreatedTime string      `json:"createdTime"`
	UpdatedTime string      `json:"updatedTime"`
	DisplayName string      `json:"displayName"`
	Description string      `json:"description"`
	Category    string      `json:"category"`
	Text        string      `json:"text"`     // the prompt body (may contain {{variables}})
	Variables   StringSlice `json:"variables"`
	Tags        StringSlice `json:"tags"`
	Model       string      `json:"model"` // optional model this prompt targets
	IsDefault   bool        `json:"isDefault"`
	State       string      `json:"state"`
}

func (p *Prompt) GetId() string {
	return fmt.Sprintf("%s/%s", p.Owner, p.Name)
}

func GetGlobalPrompts() ([]*Prompt, error) {
	prompts := []*Prompt{}
	err := findAll(adapter.db, "prompt", &prompts, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return prompts, err
	}
	return prompts, nil
}

func GetPrompts(owner string) ([]*Prompt, error) {
	prompts := []*Prompt{}
	err := findAll(adapter.db, "prompt", &prompts, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return prompts, err
	}
	return prompts, nil
}

func getPrompt(owner string, name string) (*Prompt, error) {
	prompt := Prompt{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "prompt", &prompt, pk2(prompt.Owner, prompt.Name))
	if err != nil {
		return &prompt, err
	}
	if existed {
		return &prompt, nil
	}
	return nil, nil
}

func GetPrompt(id string) (*Prompt, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getPrompt(owner, name)
}

func GetPromptCount(owner, field, value string) (int64, error) {
	q := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(q, "prompt")
}

func GetPaginationPrompts(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Prompt, error) {
	prompts := []*Prompt{}
	q := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(q, "prompt", &prompts)
	if err != nil {
		return prompts, err
	}
	return prompts, nil
}

func AddPrompt(prompt *Prompt) (bool, error) {
	prompt.CreatedTime = time.Now().Format(time.RFC3339)
	prompt.UpdatedTime = prompt.CreatedTime
	err := insertRow(adapter.db, prompt)
	if err != nil {
		return false, err
	}
	return true, nil
}

func UpdatePrompt(id string, prompt *Prompt) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	existing, err := getPrompt(owner, name)
	if err != nil {
		return false, err
	}
	if existing == nil || prompt == nil {
		return false, nil
	}
	prompt.Owner = owner
	prompt.Name = name
	prompt.UpdatedTime = time.Now().Format(time.RFC3339)
	err = adapter.db.Model(prompt).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}

func DeletePrompt(prompt *Prompt) (bool, error) {
	affected, err := deleteByPK(adapter.db, "prompt", pk2(prompt.Owner, prompt.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
