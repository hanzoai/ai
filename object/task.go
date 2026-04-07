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

	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)

type TaskResultItem struct {
	Name         string  `json:"name"`
	Score        float64 `json:"score"`
	Advantage    string  `json:"advantage"`
	Disadvantage string  `json:"disadvantage"`
	Suggestion   string  `json:"suggestion"`
}
type TaskResultCategory struct {
	Name  string            `json:"name"`
	Score float64           `json:"score"`
	Items []*TaskResultItem `json:"items"`
}
type TaskResult struct {
	Title         string                `json:"title"`
	Designer      string                `json:"designer"`
	Stage         string                `json:"stage"`
	Participants  string                `json:"participants"`
	Grade         string                `json:"grade"`
	Instructor    string                `json:"instructor"`
	Subject       string                `json:"subject"`
	School        string                `json:"school"`
	OtherSubjects string                `json:"otherSubjects"`
	Textbook      string                `json:"textbook"`
	Score         float64               `json:"score"`
	Categories    []*TaskResultCategory `json:"categories"`
}
type Task struct {
	Owner        string   `db:"pk" json:"owner"`
	Name         string   `db:"pk" json:"name"`
	CreatedTime  string   `json:"createdTime"`
	DisplayName  string   `json:"displayName"`
	Provider     string   `json:"provider"`
	Type         string   `json:"type"`
	Subject      string   `json:"subject"`
	Topic        string   `json:"topic"`
	Score        float64  `json:"score"`
	Activity     string   `json:"activity"`
	Grade        string   `json:"grade"`
	Path         string   `json:"path"`
	Scale        string   `json:"scale"`
	Example      string   `json:"example"`
	Labels       []string `json:"labels"`
	Log          string   `json:"log"`
	Result       string   `json:"result"`
	DocumentUrl  string   `json:"documentUrl"`
	DocumentText string   `json:"documentText"`
}

func GetMaskedTask(task *Task, isMaskEnabled bool) *Task {
	if !isMaskEnabled {
		return task
	}
	if task == nil {
		return nil
	}
	return task
}

func GetMaskedTasks(tasks []*Task, isMaskEnabled bool) []*Task {
	if !isMaskEnabled {
		return tasks
	}
	for _, task := range tasks {
		task = GetMaskedTask(task, isMaskEnabled)
	}
	return tasks
}

func GetGlobalTasks(owner string) ([]*Task, error) {
	tasks := []*Task{}
	q := adapter.db.Select().From("task").OrderBy("owner ASC", "created_time DESC")
	if owner != "" {
		q = q.AndWhere(dbx.HashExp{"owner": owner})
	}
	err := q.All(&tasks)
	if err != nil {
		return tasks, err
	}
	return tasks, nil
}

func GetTasks(owner string) ([]*Task, error) {
	tasks := []*Task{}
	q := adapter.db.Select().From("task").OrderBy("created_time DESC")
	if owner != "" {
		q = q.AndWhere(dbx.HashExp{"owner": owner})
	}
	err := q.All(&tasks)
	if err != nil {
		return tasks, err
	}
	return tasks, nil
}

func getTask(owner string, name string) (*Task, error) {
	task := Task{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "task", &task, pk2(task.Owner, task.Name))
	if err != nil {
		return &task, err
	}
	if existed {
		return &task, nil
	} else {
		return nil, nil
	}
}

func GetTask(id string) (*Task, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getTask(owner, name)
}

// GetTaskEffectiveScale returns rubric text: from referenced Scale.Text when Task.Scale is set.
func GetTaskEffectiveScale(task *Task) (string, error) {
	if task == nil {
		return "", fmt.Errorf("task is nil")
	}
	if task.Scale == "" {
		return "", nil
	}
	s, err := GetScale(task.Scale)
	if err != nil {
		return "", err
	}
	if s == nil {
		return "", nil
	}
	return s.Text, nil
}

func UpdateTask(id string, task *Task) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	_, err = getTask(owner, name)
	if err != nil {
		return false, err
	}
	if task == nil {
		return false, nil
	}
	task.Owner = owner
	task.Name = name
	err = adapter.db.Model(task).Update()
	if err != nil {
		return false, err
	}
	// return affected != 0
	return true, nil
}

func AddTask(task *Task) (bool, error) {
	err := insertRow(adapter.db, task)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteTask(task *Task) (bool, error) {
	affected, err := deleteByPK(adapter.db, "task", pk2(task.Owner, task.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (task *Task) GetId() string {
	return fmt.Sprintf("%s/%s", task.Owner, task.Name)
}

func GetTaskCount(owner string, field, value string) (int64, error) {
	q := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(q, "task")
}

func GetPaginationTasks(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Task, error) {
	tasks := []*Task{}
	q := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := q.All(&tasks)
	if err != nil {
		return tasks, err
	}
	return tasks, nil
}
