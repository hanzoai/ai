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

import "testing"

// Anthropic is the vendor; Claude is what it serves. A row written when the two
// were one word is renamed to the vendor — including an organization's own, which
// the seed reconciler never reaches because the seed does not name it.
func TestAProviderIsNamedByItsVendor(t *testing.T) {
	withStore(t)

	for _, p := range []*Provider{
		{Owner: "admin", Name: "anthropic", Category: "Model", Type: "Claude", State: "Active"},
		{Owner: "acme", Name: "byo-anthropic", Category: "Model", Type: "Claude", State: "Active"},
		{Owner: "acme", Name: "openai", Category: "Model", Type: "OpenAI", State: "Active"},
	} {
		if _, err := AddProvider(p); err != nil {
			t.Fatal(err)
		}
	}

	nameAnthropicByItsVendor()

	for _, want := range []struct{ owner, name, typ string }{
		{"admin", "anthropic", "Anthropic"},
		{"acme", "byo-anthropic", "Anthropic"},
		{"acme", "openai", "OpenAI"},
	} {
		got, err := getProvider(want.owner, want.name)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatalf("%s/%s is gone", want.owner, want.name)
		}
		if got.Type != want.typ {
			t.Errorf("%s/%s is typed %q, want %q", want.owner, want.name, got.Type, want.typ)
		}
	}
}
