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

	"github.com/hanzoai/cloud/bpmn"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)

type Workflow struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	DisplayName string `json:"displayName"`

	Text             string `json:"text"`
	Text2            string `json:"text2"`
	Message          string `json:"message"`
	QuestionTemplate string `json:"questionTemplate"`
}

func GetMaskedWorkflow(workflow *Workflow, isMaskEnabled bool) *Workflow {
	if !isMaskEnabled {
		return workflow
	}
	if workflow == nil {
		return nil
	}
	return workflow
}

func GetMaskedWorkflows(workflows []*Workflow, isMaskEnabled bool) []*Workflow {
	if !isMaskEnabled {
		return workflows
	}
	for _, workflow := range workflows {
		workflow = GetMaskedWorkflow(workflow, isMaskEnabled)
	}
	return workflows
}

func GetGlobalWorkflows() ([]*Workflow, error) {
	workflows := []*Workflow{}
	err := findAll(adapter.db, "workflow", &workflows, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return workflows, err
	}
	return workflows, nil
}

func GetWorkflows(owner string) ([]*Workflow, error) {
	workflows := []*Workflow{}
	err := findAll(adapter.db, "workflow", &workflows, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return workflows, err
	}
	return workflows, nil
}

func getWorkflow(owner string, name string) (*Workflow, error) {
	workflow := Workflow{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "workflow", &workflow, pk2(owner, name))
	if err != nil {
		return &workflow, err
	}
	if existed {
		return &workflow, nil
	} else {
		return nil, nil
	}
}

func GetWorkflow(id string) (*Workflow, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getWorkflow(owner, name)
}

func UpdateWorkflow(id string, workflow *Workflow, lang string) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	_, err = getWorkflow(owner, name)
	if err != nil {
		return false, err
	}
	if workflow == nil {
		return false, nil
	}

	if workflow.Text != "" && workflow.Text2 != "" {
		message := bpmn.ComparePath(workflow.Text, workflow.Text2, lang)
		workflow.Message = message
	} else {
		workflow.Message = ""
	}

	workflow.Owner = owner
	workflow.Name = name
	err = adapter.db.Model(workflow).Update()
	if err != nil {
		return false, err
	}

	return true, nil
}

func AddWorkflow(workflow *Workflow, lang string) (bool, error) {
	if workflow.Text != "" && workflow.Text2 != "" {
		message := bpmn.ComparePath(workflow.Text, workflow.Text2, lang)
		workflow.Message = message
	} else {
		workflow.Message = ""
	}

	err := insertRow(adapter.db, workflow)
	if err != nil {
		return false, err
	}

	return true, nil
}

func DeleteWorkflow(workflow *Workflow) (bool, error) {
	affected, err := deleteByPK(adapter.db, "workflow", pk2(workflow.Owner, workflow.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (workflow *Workflow) GetId() string {
	return fmt.Sprintf("%s/%s", workflow.Owner, workflow.Name)
}

func GetWorkflowCount(owner string, field, value string) (int64, error) {
	q := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(q, "workflow")
}

func GetPaginationWorkflows(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Workflow, error) {
	workflows := []*Workflow{}
	q := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(q, "workflow", &workflows)
	if err != nil {
		return workflows, err
	}
	return workflows, nil
}
