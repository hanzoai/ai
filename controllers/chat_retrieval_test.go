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
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
)

// TestRetrievalOwnerBindsToKeyNotOrigin proves the RAG tenant is bound to the
// authenticated principal / widget key — never a forgeable Origin. retrievalOwner
// no longer even accepts an Origin, so a cross-tenant Origin spoof is impossible.
func TestRetrievalOwnerBindsToKeyNotOrigin(t *testing.T) {
	t.Setenv("WIDGET_KEY_OWNERS", "hz_lux_key=lux,hz_hanzo_key=hanzo")
	t.Setenv("WIDGET_DEFAULT_OWNER", "hanzo")

	// Authenticated user => own org, always.
	if got := retrievalOwner(&iam.User{Owner: "maxpower", Name: "dave"}, "ignored"); got != "maxpower" {
		t.Errorf("authed user owner = %q, want maxpower", got)
	}

	// Widget key => the org BOUND TO THE KEY.
	if got := retrievalOwner(nil, "hz_lux_key"); got != "lux" {
		t.Errorf("widget hz_lux_key owner = %q, want lux", got)
	}
	if got := retrievalOwner(nil, "hz_hanzo_key"); got != "hanzo" {
		t.Errorf("widget hz_hanzo_key owner = %q, want hanzo", got)
	}

	// Unmapped widget key => the single safe default, NOT anything header-derived.
	if got := retrievalOwner(nil, "hz_unknown_key"); got != "hanzo" {
		t.Errorf("unmapped widget key owner = %q, want default hanzo", got)
	}

	// Non-widget, non-authed (e.g. provider key) => empty (no RAG tenant).
	if got := retrievalOwner(nil, "sk-providerkey"); got != "" {
		t.Errorf("provider-key owner = %q, want empty", got)
	}
}

// TestWidgetKeyOwnerCrossTenantIsolation is the security assertion: a widget key
// bound to tenant A can never resolve to tenant B, no matter the request context
// (there is no Origin input to influence it).
func TestWidgetKeyOwnerCrossTenantIsolation(t *testing.T) {
	t.Setenv("WIDGET_KEY_OWNERS", `{"hz_tenantA":"tenant-a","hz_tenantB":"tenant-b"}`)
	if got := widgetKeyOwner("hz_tenantA"); got != "tenant-a" {
		t.Errorf("hz_tenantA => %q, want tenant-a", got)
	}
	if got := widgetKeyOwner("hz_tenantB"); got != "tenant-b" {
		t.Errorf("hz_tenantB => %q, want tenant-b", got)
	}
}
