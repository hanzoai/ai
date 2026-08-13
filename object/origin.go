// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"net/url"
	"strings"
)

// Origin is the host that answered — the hostname of the provider's ProviderUrl.
//
// A provider's NAME is our own label for a route, and a label outlives the thing
// it names: rename the vendor, repoint the URL, and the ledger keeps writing the
// old word forever with nothing on the row able to disagree. The URL is the
// address the bytes actually went to, so a row carrying both can be asked whether
// they still agree, and the answer is a measurement rather than a belief.
//
// Only the host is kept. The path and the key are route detail; the host is the
// party on the other end, which is the question a spend row has to answer.
//
// Empty when a provider declares no URL — a family served from deployment config
// has no address to name here, and saying nothing is the honest form of that.
func (p *Provider) Origin() string {
	if p == nil {
		return ""
	}
	raw := strings.TrimSpace(p.ProviderUrl)
	if raw == "" {
		return ""
	}
	// A bare host:port parses as a path, not an authority. Give it the scheme the
	// Ollama normalization in provider.go already gives it, so "localhost:11434"
	// answers with a host instead of an empty string.
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
