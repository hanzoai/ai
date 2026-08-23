// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"github.com/hanzoai/ai/util"

	"github.com/hanzoai/dbx"
)

// RouterArtifactMeta is the published-state of a router-heads artifact: what the
// nightly retrain job last produced for a scope and whether the regression gate
// let it through. One row per scope Owner ("*" = the shared base heads the engine
// serves). It carries NO weights and NO event data — only the version tag, the
// train time, and the gate verdict/metric — so it is safe to surface on the public
// world.hanzo.ai widget. The retrain job upserts it after each run (whether it
// published the new artifact or kept the old one on a gate failure).
type RouterArtifactMeta struct {
	Owner       string `db:"pk" json:"owner"` // scope: "*" = shared base heads, else org id
	UpdatedTime string `json:"updatedTime"`

	Version     string `json:"version"`     // artifact version tag, e.g. "2026-07-16T03:00Z" or a content hash
	TrainedTime string `json:"trainedTime"` // RFC3339 when the fit ran
	Events      int    `json:"events"`      // rows the fit trained on (coverage, not content)

	// GatePassed reports whether the regression gate cleared the new artifact for
	// serving. Published is true only when GatePassed AND the engine was told to
	// reload — a gate failure keeps the OLD artifact serving and records the miss.
	GatePassed bool    `json:"gatePassed"`
	Published  bool    `json:"published"`
	GateKind   string  `json:"gateKind"`   // "routerbench" | "holdout" (honest about which ran)
	GateMetric string  `json:"gateMetric"` // e.g. "AIQ" | "holdout_reward" | "holdout_accuracy"
	GateValue  float64 `json:"gateValue"`  // the new artifact's metric
	GateBase   float64 `json:"gateBase"`   // the incumbent's metric it had to beat/match
	Note       string  `json:"note"`       // short human note (e.g. "kept incumbent: -1.8% AIQ")
}

// GetRouterArtifactMeta returns the published-state row for a scope (nil if the
// job has never run for it).
func GetRouterArtifactMeta(owner string) (*RouterArtifactMeta, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	m := RouterArtifactMeta{Owner: owner}
	existed, err := getOne(adapter.db, "router_artifact_meta", &m, dbx.HashExp{"owner": owner})
	if err != nil {
		return &m, err
	}
	if existed {
		return &m, nil
	}
	return nil, nil
}

// UpsertRouterArtifactMeta records the latest retrain outcome for a scope. Stamps
// UpdatedTime; creates the row on first write (one row per scope).
func UpsertRouterArtifactMeta(m *RouterArtifactMeta) error {
	if adapter == nil || adapter.db == nil {
		return nil
	}
	m.UpdatedTime = util.GetCurrentTime()
	existing, err := GetRouterArtifactMeta(m.Owner)
	if err != nil {
		return err
	}
	if existing == nil {
		return insertRow(adapter.db, m)
	}
	return adapter.db.Model(m).Update()
}
