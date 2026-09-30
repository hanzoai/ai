// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/dbx"
)

// RoutingVersion is one applied edit of a model family's routing catalog: the
// whole catalog as it was applied, who applied it and when, and the version it was
// made from. The rows of a family, in order, are its history; the newest is what
// the family serves, and rolling back applies an older row's catalog as a new row.
type RoutingVersion struct {
	Id          int64  `db:"pk" json:"id"`
	Family      string `json:"family"`  // "zen" | "enso"
	Catalog     string `json:"catalog"` // the family's admin catalog, JSON
	Sum         string `json:"sum"`     // sha256 of Catalog, hex
	Base        int64  `json:"base"`    // the version this one was made from; 0 for the first
	Actor       string `json:"actor"`   // owner/name of the SuperAdmin who applied it
	Note        string `json:"note"`
	Diff        string `json:"diff"` // line diff against Base, as shown when it was confirmed
	CreatedTime string `json:"createdTime"`
}

// CatalogSum is the hex sha256 of a catalog's bytes.
func CatalogSum(catalog string) string {
	s := sha256.Sum256([]byte(catalog))
	return hex.EncodeToString(s[:])
}

// AddRoutingVersion records an applied edit and returns it with its id.
func AddRoutingVersion(v *RoutingVersion) (*RoutingVersion, error) {
	if adapter == nil || adapter.db == nil {
		return nil, fmt.Errorf("routing: no store")
	}
	v.Id = 0
	v.Sum = CatalogSum(v.Catalog)
	v.CreatedTime = util.GetCurrentTime()
	if err := adapter.db.Model(v).Insert(); err != nil {
		return nil, err
	}
	return v, nil
}

// RoutingVersions are a family's newest limit versions, newest first.
func RoutingVersions(family string, limit int) ([]*RoutingVersion, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	out := []*RoutingVersion{}
	err := adapter.db.Select().From("routing_version").Where(dbx.HashExp{"family": family}).
		OrderBy("id DESC").Limit(int64(limit)).All(&out)
	return out, err
}

// LatestRoutingVersion is a family's newest version, nil when it has none.
func LatestRoutingVersion(family string) (*RoutingVersion, error) {
	vs, err := RoutingVersions(family, 1)
	if err != nil || len(vs) == 0 {
		return nil, err
	}
	return vs[0], nil
}

// GetRoutingVersion is one version by id, nil when there is none.
func GetRoutingVersion(id int64) (*RoutingVersion, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	v := RoutingVersion{}
	ok, err := getOne(adapter.db, "routing_version", &v, dbx.HashExp{"id": id})
	if err != nil || !ok {
		return nil, err
	}
	return &v, nil
}
