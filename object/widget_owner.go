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
	"encoding/json"
	"os"
	"strings"
)

// WidgetKeyOwner resolves a widget key (hz_*) to the IAM org it bills and whose
// data it may read. The owner is taken from the widget KEY itself — a per-tenant
// credential — via the WIDGET_KEY_OWNERS config (KMS first, env fallback) mapping
// key->owner. An unmapped key falls back to WIDGET_DEFAULT_OWNER (a single
// configured tenant), NEVER a header/Origin-derived org (that trust let widget
// callers read another tenant's RAG store). An empty return means the key is
// unattributable: callers MUST fail secure — refuse the request rather than spend
// the shared upstream for free, and read no tenant data.
//
// This is the ONE resolver shared by the controllers (RAG tenant isolation +
// per-org billing of widget inference) and the router balance gate, so a widget
// key means exactly the same org everywhere.
func WidgetKeyOwner(token string) string {
	if o, ok := loadWidgetKeyOwners()[token]; ok && o != "" {
		return o
	}
	return strings.TrimSpace(os.Getenv("WIDGET_DEFAULT_OWNER"))
}

// loadWidgetKeyOwners parses WIDGET_KEY_OWNERS (env first, then KMS) into a
// key->owner map. Accepts a JSON object or a comma-separated key=value list.
func loadWidgetKeyOwners() map[string]string {
	raw := os.Getenv("WIDGET_KEY_OWNERS")
	if raw == "" {
		if v, err := GetKMSSecret("WIDGET_KEY_OWNERS"); err == nil {
			raw = v
		}
	}
	out := map[string]string{}
	if raw == "" {
		return out
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		_ = json.Unmarshal([]byte(raw), &out)
		return out
	}
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return out
}
