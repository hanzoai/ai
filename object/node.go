// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

	"github.com/hanzoai/ai/util"
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
	return rowCount("node", owner, field, value)
}

func GetNodes(owner string) ([]*Node, error) {
	return rowsOf[Node]("node", owner)
}

func GetPaginationNodes(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Node, error) {
	return rowsPage[Node]("node", owner, offset, limit, field, value, sortField, sortOrder)
}

func getNode(owner string, name string) (*Node, error) {
	// An empty key names no row, and is answered without asking.
	if owner == "" || name == "" {
		return nil, nil
	}
	return getRow[Node]("node", owner, name)
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
		node.RemotePassword = SecretMask
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
	if node.RemotePassword == SecretMask {
		node.RemotePassword = p.RemotePassword
	}
	node.Owner = owner
	node.Name = name
	return updated(node)
}

func AddNode(node *Node) (bool, error) {
	return addRow(node)
}

func DeleteNode(node *Node) (bool, error) {
	return deleteRow("node", node.Owner, node.Name)
}

func (node *Node) getId() string {
	return fmt.Sprintf("%s/%s", node.Owner, node.Name)
}
