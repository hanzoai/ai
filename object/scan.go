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
	"github.com/hanzoai/dbx"
)

type Scan struct {
	Owner         string `db:"pk" json:"owner"`
	Name          string `db:"pk" json:"name"`
	CreatedTime   string `json:"createdTime"`
	UpdatedTime   string `json:"updatedTime"`
	DisplayName   string `json:"displayName"`
	TargetMode    string `json:"targetMode"`
	Target        string `json:"target"`
	Asset         string `json:"asset"`
	Provider      string `json:"provider"`
	State         string `json:"state"`
	Runner        string `json:"runner"`
	ErrorText     string `json:"errorText"`
	Command       string `json:"command"`
	RawResult     string `json:"rawResult"`
	Result        string `json:"result"`
	ResultSummary string `json:"resultSummary"`
}

func GetScanCount(owner, field, value string) (int64, error) {
	return rowCount("scan", owner, field, value)
}

func GetScans(owner string) ([]*Scan, error) {
	return rowsOf[Scan]("scan", owner)
}

func GetPaginationScans(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Scan, error) {
	return rowsPage[Scan]("scan", owner, offset, limit, field, value, sortField, sortOrder)
}

func GetScansByAsset(owner string, assetName string) ([]*Scan, error) {
	scans := []*Scan{}
	err := findAll(adapter.db, "scan", &scans, dbx.HashExp{"owner": owner, "asset": assetName}, "created_time DESC")
	if err != nil {
		return scans, err
	}
	return scans, nil
}

func getScan(owner string, name string) (*Scan, error) {
	// An empty key names no row, and is answered without asking.
	if owner == "" || name == "" {
		return nil, nil
	}
	return getRow[Scan]("scan", owner, name)
}

func GetScan(id string) (*Scan, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getScan(owner, name)
}

func UpdateScan(id string, scan *Scan) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	if _, err := getScan(owner, name); err != nil {
		return false, err
	}
	scan.Owner = owner
	scan.Name = name
	return updated(scan)
}

func AddScan(scan *Scan) (bool, error) {
	return addRow(scan)
}

func DeleteScan(scan *Scan) (bool, error) {
	return deleteRow("scan", scan.Owner, scan.Name)
}

func (scan *Scan) GetId() string {
	return fmt.Sprintf("%s/%s", scan.Owner, scan.Name)
}
