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

import (
	"strings"
	"testing"

	"github.com/hanzoai/go-openai"
)

// A vendor's rejection text is its own words, and a 401 from one names the key it
// refused — ours. wrapUpstreamError turns that text into a message the caller is
// handed, so the key comes out of it here.
func TestAnUpstreamRefusalDoesNotCarryOurKey(t *testing.T) {
	const key = "sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	wrapped := wrapUpstreamError(&openai.APIError{
		HTTPStatusCode: 401,
		Message:        "Incorrect API key provided: " + key + ". You can find your API key at https://platform.openai.com/account/api-keys.",
	})
	if wrapped == nil {
		t.Fatal("nothing came back")
	}
	if strings.Contains(wrapped.Error(), key) {
		t.Errorf("the refusal handed back our key: %s", wrapped.Error())
	}
	if !strings.Contains(wrapped.Error(), "[redacted]") {
		t.Errorf("nothing was redacted: %s", wrapped.Error())
	}
}

type stubUpstreamErr struct {
	status int
	msg    string
}

func (e *stubUpstreamErr) Error() string       { return e.msg }
func (e *stubUpstreamErr) HTTPStatusCode() int { return e.status }

var _ error = (*stubUpstreamErr)(nil)
