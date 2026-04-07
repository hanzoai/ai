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

type Caase struct {
	Owner                        string `db:"pk" json:"owner"`
	Name                         string `db:"pk" json:"name"`
	CreatedTime                  string `json:"createdTime"`
	UpdatedTime                  string `json:"updatedTime"`
	DisplayName                  string `json:"displayName"`
	Symptoms                     string `json:"symptoms"`
	Diagnosis                    string `json:"diagnosis"`
	DiagnosisDate                string `json:"diagnosisDate"`
	Prescription                 string `json:"prescription"`
	FollowUp                     string `json:"followUp"`
	Variation                    bool   `json:"variation"`
	HISInterfaceInfo             string `json:"hisInterfaceInfo"`
	PrimaryCarePhysician         string `json:"primaryCarePhysician"`
	Type                         string `json:"type"`
	PatientName                  string `json:"patientName"`
	DoctorName                   string `json:"doctorName"`
	HospitalName                 string `json:"hospitalName"`
	SpecialistAllianceId         string `json:"specialistAllianceId"`
	IntegratedCareOrganizationId string `json:"integratedCareOrganizationId"`
}

func GetCaaseCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "caase")
}

func GetCaases(owner string) ([]*Caase, error) {
	caases := []*Caase{}
	err := findAll(adapter.db, "caase", &caases, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return caases, err
	}
	return caases, nil
}

func GetPaginationCaases(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Caase, error) {
	caases := []*Caase{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "caase", &caases)
	if err != nil {
		return caases, err
	}
	return caases, nil
}

func getCaase(owner string, name string) (*Caase, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	caase := Caase{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "caase", &caase, pk2(caase.Owner, caase.Name))
	if err != nil {
		return &caase, err
	}
	if existed {
		return &caase, nil
	} else {
		return nil, nil
	}
}

func GetCaase(id string) (*Caase, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getCaase(owner, name)
}

func GetMaskedCaase(caase *Caase, errs ...error) (*Caase, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	if caase == nil {
		return nil, nil
	}
	return caase, nil
}

func GetMaskedCaases(caases []*Caase, errs ...error) ([]*Caase, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	var err error
	for _, caase := range caases {
		caase, err = GetMaskedCaase(caase)
		if err != nil {
			return nil, err
		}
	}
	return caases, nil
}

func UpdateCaase(id string, caase *Caase) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	p, err := getCaase(owner, name)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}
	caase.Owner = owner
	caase.Name = name
	err = adapter.db.Model(caase).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}

func AddCaase(caase *Caase) (bool, error) {
	err := insertRow(adapter.db, caase)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteCaase(caase *Caase) (bool, error) {
	affected, err := deleteByPK(adapter.db, "caase", pk2(caase.Owner, caase.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (caase *Caase) getId() string {
	return fmt.Sprintf("%s/%s", caase.Owner, caase.Name)
}
