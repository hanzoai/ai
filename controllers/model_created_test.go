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

package controllers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// /v1/models says when a model was released where the catalog records it — kai and
// the Jev ids from models.yaml, an OpenRouter SKU from OpenRouter's own record — and
// the listing's time for a model nothing records, as before.
func TestModelsCarryTheirReleaseTimes(t *testing.T) {
	released := map[string]int64{"kai": 1790550362, "typesafe/jev-1.13": 1789689684, "~typesafe/jev-latest": 1789689685}
	for id, at := range released {
		if r := modelRoutes[id]; r.created != at {
			t.Errorf("static %s released %d, want %d", id, r.created, at)
		}
	}

	useCatalog(t, "../conf/models.yaml")
	for id, at := range released {
		if r := resolveModelRoute(id); r == nil || r.created != at {
			t.Errorf("models.yaml %s released %v, want %d", id, r, at)
		}
	}
	before := time.Now().Unix()
	listed := map[string]modelInfo{}
	for _, m := range listAvailableModels() {
		listed[m.ID] = m
	}
	if listed["kai"].Created != released["kai"] {
		t.Fatalf("kai lists created %d, want its release %d", listed["kai"].Created, released["kai"])
	}
	for id, m := range listed {
		if _, known := released[id]; !known && m.Created < before-600 {
			t.Errorf("%s lists created %d, a time nothing records", id, m.Created)
			break
		}
	}

	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nmodels:\n  kai:\n    provider: kai\n    upstream: kai\n    released: last tuesday\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InitModelConfig(path); err == nil {
		t.Fatal("a release time that is not RFC 3339 loaded")
	}

	withOpenRouter(t, `{"data":[{"id":"typesafe/jev-router","created":1790363560,"context_length":1000,"pricing":{"prompt":"0.000001","completion":"0"}},
 {"id":"meta/undated","context_length":1000,"pricing":{"prompt":"0.000001","completion":"0"}}]}`)
	openrouterFam.fresh()
	if m, ok := openrouterFam.lookup("typesafe/jev-router"); !ok || m.releasedOr(0) != 1790363560 {
		t.Fatalf("an OpenRouter SKU's created did not carry: %+v", m)
	}
	if m, _ := openrouterFam.lookup("meta/undated"); m.releasedOr(42) != 42 {
		t.Fatal("an undated SKU was given a release time")
	}
}
