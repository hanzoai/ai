// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"net/http"
	"testing"

	"github.com/hanzoai/ai/object"
)

// A refusal a client must act on carries a name it can branch on, and the two
// refusals that read alike to a person are the two that must not read alike to a
// program: "we could not serve this" is ours to fix, "your balance is empty" is
// theirs, and only one of them is answered by adding credits.
func TestARefusalNamesItself(t *testing.T) {
	out := exhausted("some-model", []attempt{{provider: "openrouter", status: 402, err: &apiError{status: 402, msg: "Insufficient credits."}}})
	if got := statusOf(out); got != http.StatusServiceUnavailable {
		t.Errorf("every provider refused → status %d, want 503", got)
	}
	if got := codeOf(out); got != codeExhausted {
		t.Errorf("every provider refused → code %q, want %q", got, codeExhausted)
	}

	none := exhausted("some-model", nil)
	if got := codeOf(none); got != codeExhausted {
		t.Errorf("no provider configured → code %q, want %q", got, codeExhausted)
	}

	pay := billingError("balance is $%.2f", 0.0)
	if got := statusOf(pay); got != http.StatusPaymentRequired {
		t.Errorf("spend denial → status %d, want 402", got)
	}
	if got := codeOf(pay); got != object.CodeInsufficientBalance {
		t.Errorf("spend denial → code %q, want %q", got, object.CodeInsufficientBalance)
	}

	if got := codeOf(authError("who are you")); got != "" {
		t.Errorf("a failure with no stable name carries %q, want none", got)
	}
}
