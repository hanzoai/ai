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

package routers

import "testing"

func TestIsOpaqueAPIKey(t *testing.T) {
	cases := []struct {
		token string
		want  bool
		why   string
	}{
		{"pk-live-abc123", true, "publishable half"},
		{"sk-live-abc123", true, "secret half"},
		{"sk-test-abc123", true, "test-env secret"},

		{"", false, "empty is not a key"},
		{"hk-live-abc123", false, "a prefix this estate does not mint is not a key"},
		{"eyJhbGciOiJIUzI1NiJ9.e30.x", false, "JWT is handled by isJwtLike, not here"},
		{"0123456789abcdef0123456789abcdef", false, "legacy MD5 access token"},
		{"pk", false, "prefix without the hyphen is not a key"},
		{"Bearer sk-live-abc", false, "the scheme must already be stripped"},
		{"xpk-live-abc", false, "prefix must be at the START, not anywhere"},
	}
	for _, c := range cases {
		if got := isOpaqueAPIKey(c.token); got != c.want {
			t.Errorf("isOpaqueAPIKey(%q) = %v, want %v (%s)", c.token, got, c.want, c.why)
		}
	}
}
