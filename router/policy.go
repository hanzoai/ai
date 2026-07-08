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

package router

// DefaultTaskKey is the catch-all preference list consulted when a task has no
// entry of its own. Mirrors hanzo-router policy's General fallback.
const DefaultTaskKey = "default"

// Policy is the per-task model preference table plus a cost knob. It mirrors the
// declarative shape of hanzo-router's router_policy.yaml: ordered model-id
// preference per task, first usable wins, with a General/`default` catch-all.
type Policy struct {
	// Prefer maps a task tag (Task values, plus "default") to an ordered list of
	// model ids. The first entry accepted by the `known` predicate wins.
	Prefer map[string][]string
	// CostCeiling is an optional advisory per-1k cost cap forwarded to the engine
	// SLO; it does not filter the heuristic table (the table is already curated).
	CostCeiling float64
}

// ForTask returns the preferred model id for a task: the first entry in
// Prefer[task] accepted by `known`, else the first in Prefer["default"]. `known`
// (nil = accept all) lets the caller restrict the choice to models it can
// actually serve for this org. Returns "" when neither list yields a match.
func (p Policy) ForTask(task Task, known func(string) bool) string {
	if m := p.pick(string(task), known); m != "" {
		return m
	}
	return p.pick(DefaultTaskKey, known)
}

func (p Policy) pick(key string, known func(string) bool) string {
	for _, id := range p.Prefer[key] {
		if known == nil || known(id) {
			return id
		}
	}
	return ""
}
