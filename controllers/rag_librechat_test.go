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

package controllers

// Tests for the multipart embed endpoint. uploadVectors reads {status,known_type},
// and a type it cannot parse must come back as known_type=false rather than a
// 500 — the predicate that decides is pure, so it is unit-testable here.

import "testing"

func TestIsKnownType(t *testing.T) {
	cases := map[string]bool{
		"report.pdf":  true,
		"data.csv":    true,
		"notes.md":    true,
		"sheet.xlsx":  true,
		"deck.pptx":   true,
		"doc.docx":    true,
		"code.go":     true, // parsed as plain text
		"noextension": false,
	}
	for name, want := range cases {
		if got := isKnownType(name); got != want {
			t.Fatalf("isKnownType(%q) = %v, want %v", name, got, want)
		}
	}
}
