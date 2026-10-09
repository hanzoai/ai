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

package retrieval

import "testing"

func TestAskedAndStore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hdr   map[string]string
		body  string
		asked bool
		store string
	}{
		{"nothing", nil, `{"model":"zen5","store":false}`, false, ""},
		{"header on", map[string]string{"X-Retrieval": "1"}, `{}`, true, ""},
		{"header true", map[string]string{"X-Retrieval": "TRUE"}, `{}`, true, ""},
		{"header off beats body", map[string]string{"X-Retrieval": "0"}, `{"retrieval":true}`, false, ""},
		{"header store", map[string]string{"X-Retrieval-Store": "docs"}, `{}`, true, "docs"},
		{"body flag", nil, `{"retrieval":true}`, true, ""},
		{"body flag folded", nil, `{"Retrieval":true}`, true, ""},
		{"body store", nil, `{"retrieval_store":"kb"}`, true, "kb"},
		{"header store wins", map[string]string{"X-Retrieval-Store": "a"}, `{"retrieval_store":"b"}`, true, "a"},
		{"not json", nil, `retrieval:true`, false, ""},
	} {
		h := func(k string) string { return tc.hdr[k] }
		if got := Asked(h, []byte(tc.body)); got != tc.asked {
			t.Errorf("%s: Asked = %v, want %v", tc.name, got, tc.asked)
		}
		if got := Store(h, []byte(tc.body)); got != tc.store {
			t.Errorf("%s: Store = %q, want %q", tc.name, got, tc.store)
		}
	}
}
