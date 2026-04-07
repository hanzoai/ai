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

type Doctor struct {
	Owner        string `db:"pk" json:"owner"`
	Name         string `db:"pk" json:"name"`
	CreatedTime  string `json:"createdTime"`
	UpdatedTime  string `json:"updatedTime"`
	DisplayName  string `json:"displayName"`
	Department   string `json:"department"`
	Gender       string `json:"gender"`
	AccessLevel  string `json:"accessLevel"`
	HospitalName string `json:"hospitalName"`
}

func GetDoctorCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "doctor")
}

func GetDoctors(owner string) ([]*Doctor, error) {
	doctors := []*Doctor{}
	err := findAll(adapter.db, "doctor", &doctors, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return doctors, err
	}
	return doctors, nil
}

func GetPaginationDoctors(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Doctor, error) {
	doctors := []*Doctor{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "doctor", &doctors)
	if err != nil {
		return doctors, err
	}
	return doctors, nil
}

func getDoctor(owner string, name string) (*Doctor, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	doctor := Doctor{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "doctor", &doctor, pk2(doctor.Owner, doctor.Name))
	if err != nil {
		return &doctor, err
	}
	if existed {
		return &doctor, nil
	} else {
		return nil, nil
	}
}

func GetDoctor(id string) (*Doctor, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getDoctor(owner, name)
}

func GetMaskedDoctor(doctor *Doctor, errs ...error) (*Doctor, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	if doctor == nil {
		return nil, nil
	}
	return doctor, nil
}

func GetMaskedDoctors(doctors []*Doctor, errs ...error) ([]*Doctor, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	var err error
	for _, doctor := range doctors {
		doctor, err = GetMaskedDoctor(doctor)
		if err != nil {
			return nil, err
		}
	}
	return doctors, nil
}

func UpdateDoctor(id string, doctor *Doctor) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	p, err := getDoctor(owner, name)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}
	doctor.Owner = owner
	doctor.Name = name
	err = adapter.db.Model(doctor).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func AddDoctor(doctor *Doctor) (bool, error) {
	err := insertRow(adapter.db, doctor)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteDoctor(doctor *Doctor) (bool, error) {
	affected, err := deleteByPK(adapter.db, "doctor", pk2(doctor.Owner, doctor.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (doctor *Doctor) getId() string {
	return fmt.Sprintf("%s/%s", doctor.Owner, doctor.Name)
}
