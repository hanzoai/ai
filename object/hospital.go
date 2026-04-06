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
type Hospital struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
	DisplayName string `json:"displayName"`
	Address string `json:"address"`
}
func GetHospitalCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "hospital")
}
func GetHospitals(owner string) ([]*Hospital, error) {
	hospitals := []*Hospital{}
	err := findAll(adapter.db, "hospital", &hospitals, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return hospitals, err
	}
	return hospitals, nil
}
func GetPaginationHospitals(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Hospital, error) {
	hospitals := []*Hospital{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "hospital", &hospitals)
	if err != nil {
		return hospitals, err
	}
	return hospitals, nil
}
func getHospital(owner string, name string) (*Hospital, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	hospital := Hospital{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "hospital", &hospital, pk2(hospital.Owner, hospital.Name))
	if err != nil {
		return &hospital, err
	}
	if existed {
		return &hospital, nil
	} else {
		return nil, nil
	}
}
func GetHospital(id string) (*Hospital, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getHospital(owner, name)
}
func GetMaskedHospital(hospital *Hospital, errs ...error) (*Hospital, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	if hospital == nil {
		return nil, nil
	}
	return hospital, nil
}
func GetMaskedHospitals(hospitals []*Hospital, errs ...error) ([]*Hospital, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	var err error
	for _, hospital := range hospitals {
		hospital, err = GetMaskedHospital(hospital)
		if err != nil {
			return nil, err
		}
	}
	return hospitals, nil
}
func UpdateHospital(id string, hospital *Hospital) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	p, err := getHospital(owner, name)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}
	hospital.Owner = owner
	hospital.Name = name
	err = adapter.db.Model(hospital).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func AddHospital(hospital *Hospital) (bool, error) {
	err := insertRow(adapter.db, hospital)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteHospital(hospital *Hospital) (bool, error) {
	affected, err := deleteByPK(adapter.db, "hospital", pk2(hospital.Owner, hospital.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func (hospital *Hospital) getId() string {
	return fmt.Sprintf("%s/%s", hospital.Owner, hospital.Name)
}
