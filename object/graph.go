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
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)
type GraphNode struct {
	Id     string `json:"id"`
	Name   string `json:"name"`
	Value  int    `json:"val"`
	Color  string `json:"color"`
	Tag    string `json:"tag"`
	Weight int    `json:"weight"`
}
type Graph struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	DisplayName string `json:"displayName"`
	Category    string `json:"category"`
	Layout      string `json:"layout"`
	Density     int    `json:"density"`
	Store       string `json:"store"`
	StartTime   string `json:"startTime"`
	EndTime     string `json:"endTime"`
	Text        string `json:"text"`
	ErrorText   string `json:"errorText"`
}
func GetMaskedGraph(graph *Graph, isMaskEnabled bool) *Graph {
	if !isMaskEnabled {
		return graph
	}
	if graph == nil {
		return nil
	}
	return graph
}
func GetMaskedGraphs(graphs []*Graph, isMaskEnabled bool) []*Graph {
	if !isMaskEnabled {
		return graphs
	}
	for _, graph := range graphs {
		graph = GetMaskedGraph(graph, isMaskEnabled)
	}
	return graphs
}
func GetGlobalGraphs() ([]*Graph, error) {
	graphs := []*Graph{}
	err := findAll(adapter.db, "graph", &graphs, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return graphs, err
	}
	return graphs, nil
}
func GetGraphs(owner string) ([]*Graph, error) {
	graphs := []*Graph{}
	err := findAll(adapter.db, "graph", &graphs, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return graphs, err
	}
	return graphs, nil
}
func getGraph(owner string, name string) (*Graph, error) {
	graph := Graph{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "graph", &graph, pk2(graph.Owner, graph.Name))
	if err != nil {
		return &graph, err
	}
	if existed {
		return &graph, nil
	} else {
		return nil, nil
	}
}
func GetGraph(id string) (*Graph, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getGraph(owner, name)
}
func UpdateGraph(id string, graph *Graph) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	_, err = getGraph(owner, name)
	if err != nil {
		return false, err
	}
	if graph == nil {
		return false, nil
	}
	graph.Owner = owner
	graph.Name = name
	err = adapter.db.Model(graph).Update()
	if err != nil {
		return false, err
	}
	// return affected != 0
	return true, nil
}
func AddGraph(graph *Graph) (bool, error) {
	err := insertRow(adapter.db, graph)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteGraph(graph *Graph) (bool, error) {
	affected, err := deleteByPK(adapter.db, "graph", pk2(graph.Owner, graph.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func (graph *Graph) GetId() string {
	return fmt.Sprintf("%s/%s", graph.Owner, graph.Name)
}
func GetGraphCount(owner string, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "graph")
}
func GetPaginationGraphs(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Graph, error) {
	graphs := []*Graph{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "graph", &graphs)
	if err != nil {
		return graphs, err
	}
	return graphs, nil
}
