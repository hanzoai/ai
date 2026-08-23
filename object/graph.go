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

	"github.com/hanzoai/ai/util"
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
	return allRows[Graph]("graph")
}

func GetGraphs(owner string) ([]*Graph, error) {
	return rowsOf[Graph]("graph", owner)
}

func getGraph(owner, name string) (*Graph, error) {
	return getRow[Graph]("graph", owner, name)
}

func GetGraph(id string) (*Graph, error) {
	return rowAt[Graph]("graph", id)
}

func UpdateGraph(id string, graph *Graph) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	if graph == nil {
		return false, nil
	}
	graph.Owner, graph.Name = owner, name
	return updated(graph)
}

func AddGraph(graph *Graph) (bool, error) {
	return addRow(graph)
}

func DeleteGraph(graph *Graph) (bool, error) {
	return deleteRow("graph", graph.Owner, graph.Name)
}

func (graph *Graph) GetId() string {
	return fmt.Sprintf("%s/%s", graph.Owner, graph.Name)
}

func GetGraphCount(owner string, field, value string) (int64, error) {
	return rowCount("graph", owner, field, value)
}

func GetPaginationGraphs(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Graph, error) {
	return rowsPage[Graph]("graph", owner, offset, limit, field, value, sortField, sortOrder)
}
