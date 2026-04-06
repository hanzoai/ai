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
type Patient struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
	DisplayName string `json:"displayName"`
	Gender  string `json:"gender"`
	Address string `json:"address"`
	Email   string `json:"email"`
	BloodType    string   `json:"bloodType"`
	Allergies    string   `json:"allergies"`
	Owners       []string `json:"owners"`
	HospitalName string   `json:"hospitalName"`
}
func GetPatientCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "patient")
}
func GetPatients(owner string) ([]*Patient, error) {
	patients := []*Patient{}
	err := findAll(adapter.db, "patient", &patients, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return patients, err
	}
	return patients, nil
}
func GetPaginationPatients(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Patient, error) {
	patients := []*Patient{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "patient", &patients)
	if err != nil {
		return patients, err
	}
	return patients, nil
}
func getPatient(owner string, name string) (*Patient, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	patient := Patient{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "patient", &patient, pk2(patient.Owner, patient.Name))
	if err != nil {
		return &patient, err
	}
	if existed {
		return &patient, nil
	} else {
		return nil, nil
	}
}
func GetPatient(id string) (*Patient, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getPatient(owner, name)
}
func GetMaskedPatient(patient *Patient, errs ...error) (*Patient, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	if patient == nil {
		return nil, nil
	}
	return patient, nil
}
func GetMaskedPatients(patients []*Patient, errs ...error) ([]*Patient, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	var err error
	for _, patient := range patients {
		patient, err = GetMaskedPatient(patient)
		if err != nil {
			return nil, err
		}
	}
	return patients, nil
}
func UpdatePatient(id string, patient *Patient) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	p, err := getPatient(owner, name)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}
	patient.Owner = owner
	patient.Name = name
	err = adapter.db.Model(patient).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func AddPatient(patient *Patient) (bool, error) {
	err := insertRow(adapter.db, patient)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeletePatient(patient *Patient) (bool, error) {
	affected, err := deleteByPK(adapter.db, "patient", pk2(patient.Owner, patient.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func (patient *Patient) getId() string {
	return fmt.Sprintf("%s/%s", patient.Owner, patient.Name)
}
