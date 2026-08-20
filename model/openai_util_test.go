// Copyright 2026 The Hanzo Authors. All Rights Reserved.

package model

import "testing"

// The Responses API answers 400 and drops the whole call when a reasoning model
// is handed `temperature` — so the families that refuse it must be recognised
// by name, prefixed or namespaced.
func TestSamplesFreely(t *testing.T) {
	for _, m := range []string{"gpt-5", "gpt-5.3-codex", "o3", "o3-mini", "openai-direct/gpt-5", "O1-preview"} {
		if samplesFreely(m) {
			t.Errorf("%s: reasoning model must NOT be sent temperature/top_p", m)
		}
	}
	for _, m := range []string{"gpt-4o", "gpt-4.1", "openai-direct/gpt-4o", "claude-sonnet-4-6", "zen5-coder"} {
		if !samplesFreely(m) {
			t.Errorf("%s: ordinary model must keep its sampling knobs", m)
		}
	}
}
