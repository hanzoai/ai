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

package controllers

import "testing"

// A model is called at the endpoint its outputs name: text, alone or beside audio or an
// image, is a chat; every other kind of answer has its own; a model that states none
// lists none rather than a guess.
func TestEndpointOf(t *testing.T) {
	for _, tc := range []struct {
		outputs []string
		want    string
	}{
		{[]string{"text"}, "/v1/chat/completions"},
		{[]string{"text", "audio"}, "/v1/chat/completions"},
		{[]string{"image", "text"}, "/v1/chat/completions"},
		{[]string{"decision"}, "/v1/decisions"},
		{[]string{"embeddings"}, "/v1/embeddings"},
		{[]string{"rerank"}, "/v1/rerank"},
		{[]string{"speech"}, "/v1/audio/speech"},
		{[]string{"transcript"}, "/v1/audio/transcriptions"},
		{[]string{"video"}, "/v1/videos/generations"},
		{[]string{"image"}, "/v1/images/generations"},
		{[]string{"audio"}, ""},
		{nil, ""},
	} {
		if got := endpointOf(tc.outputs); got != tc.want {
			t.Errorf("endpointOf(%v) = %q, want %q", tc.outputs, got, tc.want)
		}
	}
}
