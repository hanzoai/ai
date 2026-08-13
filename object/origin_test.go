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

import "testing"

// TestOriginIsTheHost: a provider's name is a word we chose and can keep saying
// after it stops being true; its URL is where the bytes go. Origin reduces the URL
// to the party at the other end — no path, no key, no scheme — because "who
// answered" is the only question a spend row needs it for.
func TestOriginIsTheHost(t *testing.T) {
	for _, c := range []struct {
		url  string
		want string
		why  string
	}{
		{"https://inference.do-ai.run/v1", "inference.do-ai.run", "the path is route detail"},
		{"https://openrouter.ai/api/v1", "openrouter.ai", ""},
		{"https://api.openai.com/v1", "api.openai.com", ""},
		{"http://speech.hanzo.svc.cluster.local:8000", "speech.hanzo.svc.cluster.local",
			"the port is not the party"},
		{"localhost:11434", "localhost", "a bare host:port is a host, not a path"},
		{"", "", "a provider with no URL has no address to name"},
		{"   ", "", "and neither does a blank one"},
		{"://nonsense", "", "unparseable says nothing rather than guessing"},
	} {
		p := &Provider{Name: "any", ProviderUrl: c.url}
		if got := p.Origin(); got != c.want {
			t.Errorf("Origin(%q) = %q, want %q — %s", c.url, got, c.want, c.why)
		}
	}

	// The zero provider is reachable from a failed route, where nothing answered.
	if got := (*Provider)(nil).Origin(); got != "" {
		t.Errorf("a nil provider answered %q; nothing served, so nothing is the answer", got)
	}
}
