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
	if o, ok := loadKeyMap("WIDGET_KEY_OWNERS")[token]; ok && o != "" {
		return o
	}
	return strings.TrimSpace(os.Getenv("WIDGET_DEFAULT_OWNER"))
}

// WidgetKeyModels resolves a widget key (hz_*) to the EXPLICIT set of model ids it
// may call, from the WIDGET_KEY_MODELS config (KMS first, env fallback) mapping
// key->model list. It is the per-key model allowlist: it binds a public widget key
// to exactly the models its owner provisioned — the docs key to `enso` only — so a
// leaked key cannot reach arbitrary (or expensive) models the coarse global widget
// allowlist happens to permit. Model ids are separated by space or comma and
// lowercased for case-insensitive matching (mirrors resolveModelRoute). An empty
// return means the key declares NO explicit binding: the caller falls back to the
// global widget allowlist. Values are the SAME map shape as WIDGET_KEY_OWNERS, so a
// key is configured the one way everywhere.
func WidgetKeyModels(token string) []string {
	raw := strings.TrimSpace(loadKeyMap("WIDGET_KEY_MODELS")[token])
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if m := strings.ToLower(strings.TrimSpace(f)); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// loadKeyMap parses a per-widget-key binding (env first, then KMS under the same
// name) into a key->value map. Accepts a JSON object or a comma-separated
// key=value list. It is the ONE parser shared by every widget-key binding
// (WIDGET_KEY_OWNERS owner, WIDGET_KEY_MODELS allowlist), so a key is expressed the
// same way for each dimension it carries.
func loadKeyMap(name string) map[string]string {
	raw := os.Getenv(name)
	if raw == "" {
		if v, err := GetKMSSecret(name); err == nil {
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
