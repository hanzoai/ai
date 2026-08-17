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

package ai

// WHAT MOUNTING MEANS NOW.
//
// Three tests used to live here and all three drove the strangler: a stub handler
// installed through SetHandler, and assertions that a /v1/* request reached it with
// its path unchanged and its trace context intact. Every part of that is gone —
// there is no stub to install, no glob to forward through, and no net/http boundary
// to carry a context across, because each address is its own route reaching its own
// controller.
//
// What is left worth holding is the honest answer before the runtime exists.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request that arrives before the app is built is answered 503, not 500 and not a
// silence.
//
// This is the ZAP transports' door — they take the HTTP surface as an http.Handler
// — and it can be asked before ai.App has stored anything. The honest answer is that
// the service cannot answer yet, which a caller can retry; the failure to avoid is a
// nil dereference dressed as a 500.
func TestHandlerRefusesBeforeTheAppIsBuilt(t *testing.T) {
	built.Store(nil)

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 before the runtime exists", rec.Code)
	}
}
