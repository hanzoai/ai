// Copyright 2025 The Casibase Authors. All Rights Reserved.
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

// Scale state values for visibility (which scales are included in public lists).
const (
	ScaleStatePublic = "Public"
	ScaleStateHidden = "Hidden"
)

// Scale is a reusable rubric / evaluation scale (量表), referenced by tasks via Task.Scale (owner/name id).
type Scale struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	DisplayName string `json:"displayName"`
	Text        string `json:"text"`
	State       string `json:"state"`
}

func GetMaskedScale(scale *Scale, isMaskEnabled bool) *Scale {
	if !isMaskEnabled {
		return scale
	}
	if scale == nil {
		return nil
	}
	return scale
}

func GetMaskedScales(scales []*Scale, isMaskEnabled bool) []*Scale {
	if !isMaskEnabled {
		return scales
	}
	for _, s := range scales {
		s = GetMaskedScale(s, isMaskEnabled)
	}
	return scales
}

func GetGlobalScales() ([]*Scale, error) {
	scales := []*Scale{}
	err := findAll(adapter.db, "scale", &scales, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return scales, err
	}
	return scales, nil
}

func GetScales(owner string) ([]*Scale, error) {
	scales := []*Scale{}
	var where dbx.Expression
	if owner != "" {
		where = dbx.HashExp{"owner": owner}
	}
	err := findAll(adapter.db, "scale", &scales, where, "created_time DESC")
	if err != nil {
		return scales, err
	}
	return scales, nil
}

// GetPublicScales returns scales visible to non-admins (Public or empty state).
func GetPublicScales(owner string) ([]*Scale, error) {
	scales := []*Scale{}
	err := adapter.db.Select().From("scale").Where(dbx.NewExp("owner = {:p0} AND (state = {:p1} OR state = '' OR state IS NULL)", dbx.Params{"p0": owner, "p1": ScaleStatePublic})).OrderBy("created_time DESC").All(&scales)
	if err != nil {
		return scales, err
	}
	return scales, nil
}

func getScale(owner string, name string) (*Scale, error) {
	s := Scale{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "scale", &s, pk2(s.Owner, s.Name))
	if err != nil {
		return &s, err
	}
	if existed {
		return &s, nil
	}
	return nil, nil
}

func GetScale(id string) (*Scale, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getScale(owner, name)
}

func UpdateScale(id string, scale *Scale) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	_, err = getScale(owner, name)
	if err != nil {
		return false, err
	}
	if scale == nil {
		return false, nil
	}
	scale.Owner = owner
	scale.Name = name
	err = adapter.db.Model(scale).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}

func AddScale(scale *Scale) (bool, error) {
	err := insertRow(adapter.db, scale)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteScale(scale *Scale) (bool, error) {
	affected, err := deleteByPK(adapter.db, "scale", pk2(scale.Owner, scale.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (s *Scale) GetId() string {
	return fmt.Sprintf("%s/%s", s.Owner, s.Name)
}

func GetScaleCount(owner string, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "scale")
}

func GetPaginationScales(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Scale, error) {
	scales := []*Scale{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "scale", &scales)
	if err != nil {
		return scales, err
	}
	return scales, nil
}
