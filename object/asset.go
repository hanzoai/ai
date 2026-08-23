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
		maskedAsset.Password = SecretMask
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
	return rowCount("asset", owner, field, value)
}

func GetAssets(owner string) ([]*Asset, error) {
	return rowsOf[Asset]("asset", owner)
}

func GetPaginationAssets(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Asset, error) {
	return rowsPage[Asset]("asset", owner, offset, limit, field, value, sortField, sortOrder)
}

func getAsset(owner string, name string) (*Asset, error) {
	// An empty key names no row, and is answered without asking.
	if owner == "" || name == "" {
		return nil, nil
	}
	return getRow[Asset]("asset", owner, name)
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
	return addRow(asset)
}

func addAssets(assets []*Asset) (bool, error) {
	return addRow(assets)
}

func DeleteAsset(asset *Asset) (bool, error) {
	return deleteRow("asset", asset.Owner, asset.Name)
}

func (a *Asset) processAssetParams(assetDb *Asset) {
	if a.Password == SecretMask {
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
