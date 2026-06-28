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
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestStatusOf asserts each typed apiError maps to its HTTP status, and that an
// untyped error fails SECURE (401 deny), never 200 (grant). This is the contract
// the OpenAI-compatible auth surface depends on: unknown/invalid key 401,
// insufficient balance 402, bad model 400, server misconfig 500.
func TestStatusOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"authError", authError("invalid API key"), http.StatusUnauthorized},
		{"billingError", billingError("balance is $0.00"), http.StatusPaymentRequired},
		{"modelError", modelError("model %q not available", "gpt-4o-mini"), http.StatusBadRequest},
		{"serverError", serverError("provider not configured"), http.StatusInternalServerError},
		{"untyped defaults to 401", errors.New("boom"), http.StatusUnauthorized},
		{"wrapped-typed preserved via errors.As", fmt.Errorf("ctx: %w", billingError("x")), http.StatusPaymentRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusOf(tc.err); got != tc.want {
				t.Fatalf("statusOf(%v) = %d, want %d", tc.err, got, tc.want)
			}
			if statusOf(tc.err) == http.StatusOK {
				t.Fatalf("statusOf must never return 200 (grant on failure)")
			}
		})
	}
}

// TestWrapAuth asserts wrapAuth tags an untyped error as 401 but leaves a typed
// apiError (the 400/402/500 produced deeper in the routing policy) untouched, so
// a valid key + bad model still surfaces as 400 through the IAM/JWT branches.
func TestWrapAuth(t *testing.T) {
	if wrapAuth(nil) != nil {
		t.Fatal("wrapAuth(nil) must be nil")
	}

	// Untyped -> 401, message preserved.
	wrapped := wrapAuth(errors.New("opaque provider key rejected"))
	if statusOf(wrapped) != http.StatusUnauthorized {
		t.Fatalf("wrapAuth(untyped) status = %d, want 401", statusOf(wrapped))
	}
	if wrapped.Error() != "opaque provider key rejected" {
		t.Fatalf("wrapAuth must preserve message, got %q", wrapped.Error())
	}

	// Typed -> unchanged status (NOT downgraded to 401).
	for _, tc := range []struct {
		in   error
		want int
	}{
		{modelError("bad model"), http.StatusBadRequest},
		{billingError("no funds"), http.StatusPaymentRequired},
		{serverError("misconfig"), http.StatusInternalServerError},
	} {
		if got := statusOf(wrapAuth(tc.in)); got != tc.want {
			t.Fatalf("wrapAuth(typed %d) downgraded to %d", tc.want, got)
		}
	}
}
