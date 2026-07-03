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

package conf

import (
	"os"
	"testing"
)

// TestDisablePreviewModeEnvLever proves the DISABLE_PREVIEW_MODE env override is
// honored env-first — the reversible, no-rebuild lever ops flips on the deployed
// cloud CR to close the preview-mode surface (F1). "true" disables preview
// (preview OFF); any other explicit value keeps it enabled.
func TestDisablePreviewModeEnvLever(t *testing.T) {
	orig, had := os.LookupEnv("DISABLE_PREVIEW_MODE")
	t.Cleanup(func() {
		if had {
			os.Setenv("DISABLE_PREVIEW_MODE", orig)
		} else {
			os.Unsetenv("DISABLE_PREVIEW_MODE")
		}
	})

	cases := []struct {
		env         string
		wantDisable bool
	}{
		{"true", true},
		{"TRUE", true},
		{" true ", true}, // trimmed
		{"false", false},
		{"", false},   // empty is not "true" → preview stays ON (fail toward dev default, not toward disabling)
		{"1", false},  // only the literal "true" flips it — no ambiguity
		{"yes", false},
	}
	for _, tc := range cases {
		os.Setenv("DISABLE_PREVIEW_MODE", tc.env)
		if got := DisablePreviewMode(); got != tc.wantDisable {
			t.Errorf("DISABLE_PREVIEW_MODE=%q → DisablePreviewMode()=%v, want %v", tc.env, got, tc.wantDisable)
		}
		// IsPreviewMode is exactly the negation — the semantic callers use.
		if got := IsPreviewMode(); got != !tc.wantDisable {
			t.Errorf("DISABLE_PREVIEW_MODE=%q → IsPreviewMode()=%v, want %v", tc.env, got, !tc.wantDisable)
		}
	}
}

// TestDisablePreviewModeEnvBeatsFile locks that the env is consulted FIRST, so a
// deployed binary (whose app.conf, if any, sets disablePreviewMode=false) can be
// flipped to preview-OFF purely by env — no image rebuild.
func TestDisablePreviewModeEnvBeatsFile(t *testing.T) {
	orig, had := os.LookupEnv("DISABLE_PREVIEW_MODE")
	t.Cleanup(func() {
		if had {
			os.Setenv("DISABLE_PREVIEW_MODE", orig)
		} else {
			os.Unsetenv("DISABLE_PREVIEW_MODE")
		}
	})

	os.Setenv("DISABLE_PREVIEW_MODE", "true")
	if IsPreviewMode() {
		t.Fatal("with DISABLE_PREVIEW_MODE=true, IsPreviewMode() must be false regardless of app.conf")
	}
	if !DisablePreviewMode() {
		t.Fatal("with DISABLE_PREVIEW_MODE=true, DisablePreviewMode() must be true regardless of app.conf")
	}
}
