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
type Scan struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
	DisplayName string `json:"displayName"`
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
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "scan")
}
func GetScans(owner string) ([]*Scan, error) {
	scans := []*Scan{}
	err := findAll(adapter.db, "scan", &scans, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return scans, err
	}
	return scans, nil
}
func GetPaginationScans(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Scan, error) {
	scans := []*Scan{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "scan", &scans)
	if err != nil {
		return scans, err
	}
	return scans, nil
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
	if owner == "" || name == "" {
		return nil, nil
	}
	scan := Scan{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "scan", &scan, pk2(scan.Owner, scan.Name))
	if err != nil {
		return &scan, err
	}
	if existed {
		return &scan, nil
	} else {
		return nil, nil
	}
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
	err = adapter.db.Model(scan).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func AddScan(scan *Scan) (bool, error) {
	err := insertRow(adapter.db, scan)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteScan(scan *Scan) (bool, error) {
	affected, err := deleteByPK(adapter.db, "scan", pk2(scan.Owner, scan.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func (scan *Scan) GetId() string {
	return fmt.Sprintf("%s/%s", scan.Owner, scan.Name)
}
