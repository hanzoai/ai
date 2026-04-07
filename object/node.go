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

type Service struct {
	No             int    `json:"no"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	Port           int    `json:"port"`
	ProcessId      int    `json:"processId"`
	ExpectedStatus string `json:"expectedStatus"`
	Status         string `json:"status"`
	SubStatus      string `json:"subStatus"`
	Message        string `json:"message"`
}
type Patch struct {
	Name           string `json:"name"`
	Category       string `json:"category"`
	Title          string `json:"title"`
	Url            string `json:"url"`
	Size           string `json:"size"`
	ExpectedStatus string `json:"expectedStatus"`
	Status         string `json:"status"`
	InstallTime    string `json:"installTime"`
	Message        string `json:"message"`
}
type RemoteApp struct {
	No            int    `json:"no"`
	RemoteAppName string `json:"remoteAppName"`
	RemoteAppDir  string `json:"remoteAppDir"`
	RemoteAppArgs string `json:"remoteAppArgs"`
}
type Node struct {
	Owner           string       `db:"pk" json:"owner"`
	Name            string       `db:"pk" json:"name"`
	CreatedTime     string       `json:"createdTime"`
	UpdatedTime     string       `json:"updatedTime"`
	DisplayName     string       `json:"displayName"`
	Description     string       `json:"description"`
	Category        string       `json:"category"`
	Type            string       `json:"type"`
	Tag             string       `json:"tag"`
	MachineName     string       `json:"machineName"`
	Os              string       `json:"os"`
	PublicIp        string       `json:"publicIp"`
	PrivateIp       string       `json:"privateIp"`
	Size            string       `json:"size"`
	CpuSize         string       `json:"cpuSize"`
	MemSize         string       `json:"memSize"`
	RemoteProtocol  string       `json:"remoteProtocol"`
	RemotePort      int          `json:"remotePort"`
	RemoteUsername  string       `json:"remoteUsername"`
	RemotePassword  string       `json:"remotePassword"`
	AutoQuery       bool         `json:"autoQuery"`
	IsPermanent     bool         `json:"isPermanent"`
	Language        string       `json:"language"`
	EnableRemoteApp bool         `json:"enableRemoteApp"`
	RemoteApps      []*RemoteApp `json:"remoteApps"`
	Services        []*Service   `json:"services"`
	Patches         []*Patch     `json:"patches"`
}

func GetNodeCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "node")
}

func GetNodes(owner string) ([]*Node, error) {
	nodes := []*Node{}
	err := findAll(adapter.db, "node", &nodes, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return nodes, err
	}
	return nodes, nil
}

func GetPaginationNodes(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Node, error) {
	nodes := []*Node{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "node", &nodes)
	if err != nil {
		return nodes, err
	}
	return nodes, nil
}

func getNode(owner string, name string) (*Node, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	node := Node{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "node", &node, pk2(node.Owner, node.Name))
	if err != nil {
		return &node, err
	}
	if existed {
		return &node, nil
	} else {
		return nil, nil
	}
}

func GetNode(id string) (*Node, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getNode(owner, name)
}

func GetMaskedNode(node *Node, errs ...error) (*Node, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	if node == nil {
		return nil, nil
	}
	if node.RemotePassword != "" {
		node.RemotePassword = "***"
	}
	return node, nil
}

func GetMaskedNodes(nodes []*Node, errs ...error) ([]*Node, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	var err error
	for _, node := range nodes {
		node, err = GetMaskedNode(node)
		if err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

func UpdateNode(id string, node *Node) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	p, err := getNode(owner, name)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}
	if node.RemotePassword == "***" {
		node.RemotePassword = p.RemotePassword
	}
	node.Owner = owner
	node.Name = name
	err = adapter.db.Model(node).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func AddNode(node *Node) (bool, error) {
	err := insertRow(adapter.db, node)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteNode(node *Node) (bool, error) {
	affected, err := deleteByPK(adapter.db, "node", pk2(node.Owner, node.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (node *Node) getId() string {
	return fmt.Sprintf("%s/%s", node.Owner, node.Name)
}
