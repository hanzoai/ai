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
	"strings"

	"github.com/hanzoai/ai/conf"
)

// ZenProvider synthesizes the virtual "zen" model provider from deployment
// configuration (ZEN_URL / ZEN_API_KEY), or nil when zen is not configured.
//
// Zen is not a database row: its address is configuration, so this repository
// carries no zen routing detail and reads as open source. Because it resolves the
// same way a real Model provider does — a name, a URL, a key — every path that
// looks a provider up by name serves the zen family without a special case beyond
// the one lookup in GetModelProviderByName. See hip-00NN.
func ZenProvider() *Provider {
	base := strings.TrimRight(strings.TrimSpace(conf.GetConfigString("ZEN_URL")), "/")
	if base == "" {
		return nil
	}
	return &Provider{
		Owner:        "admin",
		Name:         "zen",
		Category:     "Model",
		Type:         "Zen",
		State:        "Active",
		ProviderUrl:  base,
		ClientSecret: strings.TrimSpace(conf.GetConfigString("ZEN_API_KEY")),
	}
}
