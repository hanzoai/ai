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

type Asset struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
	DisplayName string `json:"displayName"`
	Provider    string `json:"provider"`
	Id          string `json:"id"`
	Type        string `json:"type"`
	Region      string `json:"region"`
	Zone        string `json:"zone"`
	State       string `json:"state"`
	Tag         string `json:"tag"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Properties  string `json:"properties"`
}

func GetMaskedAsset(asset *Asset, isMaskEnabled bool) *Asset {
	if !isMaskEnabled {
		return asset
	}
	if asset == nil {
		return nil
	}
	// Create a copy to avoid modifying the original
	maskedAsset := *asset
	if maskedAsset.Password != "" {
		maskedAsset.Password = "***"
	}
	return &maskedAsset
}

func GetMaskedAssets(assets []*Asset, isMaskEnabled bool) []*Asset {
	if !isMaskEnabled {
		return assets
	}
	for i := range assets {
		assets[i] = GetMaskedAsset(assets[i], isMaskEnabled)
	}
	return assets
}

func GetAssetCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "asset")
}

func GetAssets(owner string) ([]*Asset, error) {
	assets := []*Asset{}
	err := findAll(adapter.db, "asset", &assets, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return assets, err
	}
	return assets, nil
}

func GetPaginationAssets(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Asset, error) {
	assets := []*Asset{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "asset", &assets)
	if err != nil {
		return assets, err
	}
	return assets, nil
}

func getAsset(owner string, name string) (*Asset, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	asset := Asset{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "asset", &asset, pk2(asset.Owner, asset.Name))
	if err != nil {
		return &asset, err
	}
	if existed {
		return &asset, nil
	} else {
		return nil, nil
	}
}

func GetAsset(id string) (*Asset, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getAsset(owner, name)
}

func UpdateAsset(id string, asset *Asset) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	assetDb, err := getAsset(owner, name)
	if err != nil {
		return false, err
	}
	if assetDb == nil {
		return false, nil
	}
	asset.processAssetParams(assetDb)
	asset.Owner = owner
	asset.Name = name
	err = adapter.db.Model(asset).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}

func AddAsset(asset *Asset) (bool, error) {
	err := insertRow(adapter.db, asset)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func addAssets(assets []*Asset) (bool, error) {
	err := insertRow(adapter.db, assets)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteAsset(asset *Asset) (bool, error) {
	affected, err := deleteByPK(adapter.db, "asset", pk2(asset.Owner, asset.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func deleteAssets(owner string) (bool, error) {
	affected, err := deleteWhere(adapter.db, "asset", dbx.HashExp{"owner": owner})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (a *Asset) processAssetParams(assetDb *Asset) {
	if a.Password == "***" {
		a.Password = assetDb.Password
	}
}

func (asset *Asset) GetId() string {
	return fmt.Sprintf("%s/%s", asset.Owner, asset.Name)
}

func (asset *Asset) GetScanTarget() (string, error) {
	if asset.Type == "Virtual Machine" {
		publicIp, err := util.GetFieldFromJsonString(asset.Properties, "publicIp")
		if err != nil {
			return "", fmt.Errorf("failed to parse publicIp from properties: %v", err)
		}
		if publicIp != "" {
			return publicIp, nil
		}
		// Fallback to asset.Id if publicIp is not available
		return asset.Id, nil
	}
	// For non-Virtual Machine types, use asset.Id
	return asset.Id, nil
}
