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
type Vector struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	DisplayName string  `json:"displayName"`
	Store       string  `json:"store"`
	Provider    string  `json:"provider"`
	File        string  `json:"file"`
	Index       int     `json:"index"`
	Text        string  `json:"text"`
	TokenCount  int     `json:"tokenCount"`
	Price       float64 `json:"price"`
	Currency    string  `json:"currency"`
	Score       float32 `json:"score"`
	Data      []float32 `json:"data"`
	Dimension int       `json:"dimension"`
}
func GetGlobalVectors() ([]*Vector, error) {
	vectors := []*Vector{}
	err := findAll(adapter.db, "vector", &vectors, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return vectors, err
	}
	return vectors, nil
}
func GetVectors(owner string) ([]*Vector, error) {
	vectors := []*Vector{}
	err := findAll(adapter.db, "vector", &vectors, dbx.HashExp{"owner": owner}, "file ASC", "index ASC")
	if err != nil {
		return vectors, err
	}
	return vectors, nil
}
func getVectorsByProvider(relatedStores []string, provider string) ([]*Vector, error) {
	vectors := []*Vector{}
	err := adapter.db.Select().From("vector").Where(dbx.And(dbx.In("store", toInterfaceSlice(relatedStores)...), dbx.HashExp{"provider": provider})).All(&vectors)
	if err != nil {
		return vectors, err
	}
	return vectors, nil
}
func getVector(owner string, name string) (*Vector, error) {
	vector := Vector{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "vector", &vector, pk2(vector.Owner, vector.Name))
	if err != nil {
		return &vector, err
	}
	if existed {
		return &vector, nil
	} else {
		return nil, nil
	}
}
func getVectorByIndex(owner string, store string, file string, index int) (*Vector, error) {
	vector := Vector{Owner: owner, Store: store, File: file, Index: index}
	existed, err := getOne(adapter.db, "vector", &vector, pk2(vector.Owner, vector.Name))
	if err != nil {
		return &vector, err
	}
	if existed {
		return &vector, nil
	} else {
		return nil, nil
	}
}
func GetVector(id string) (*Vector, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getVector(owner, name)
}
func UpdateVector(id string, vector *Vector, lang string) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	oldVector, err := getVector(owner, name)
	if err != nil {
		return false, err
	}
	if vector == nil {
		return false, nil
	}
	if oldVector.Text != vector.Text {
		if vector.Text == "" {
			vector.Data = []float32{}
		} else {
			_, err = refreshVector(vector, lang)
			if err != nil {
				return false, err
			}
		}
	}
	vector.Owner = owner
	vector.Name = name
	err = adapter.db.Model(vector).Update()
	if err != nil {
		return false, err
	}
	// return affected != 0
	return true, nil
}
func AddVector(vector *Vector) (bool, error) {
	//err := Index.Add(util.GetId(vector.Owner, vector.Name), vector.Data)
	//if err != nil {
	//	return false, err
	//}
	err := insertRow(adapter.db, vector)
	if err != nil {
		return false, err
	}
	return true, nil
}
func DeleteVector(vector *Vector) (bool, error) {
	affected, err := deleteByPK(adapter.db, "vector", pk2(vector.Owner, vector.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteVectorsByStore(owner string, storeName string) (bool, error) {
	affected, err := deleteWhere(adapter.db, "vector", dbx.NewExp("owner = ? AND store = ?", dbx.Params{"p0": owner, "p1": storeName}))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteVectorsByFile(owner string, storeName string, fileKey string) (bool, error) {
	affected, err := deleteWhere(adapter.db, "vector", dbx.NewExp("owner = ? AND store = ? AND file = ?", dbx.Params{"p0": owner, "p1": storeName, "p2": fileKey}))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func (vector *Vector) GetId() string {
	return fmt.Sprintf("%s/%s", vector.Owner, vector.Name)
}
func GetVectorCount(owner string, storeName string, field string, value string) (int64, error) {
	q := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(q, "vector")
}
func GetPaginationVectors(owner string, storeName string, offset, limit int, field, value, sortField, sortOrder string) ([]*Vector, error) {
	vectors := []*Vector{}
	q := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	if storeName != "" {
		q = q.AndWhere(dbx.HashExp{"store": storeName})
	}
	err := queryFind(q, "vector", &vectors)
	if err != nil {
		return vectors, err
	}
	return vectors, nil
}
