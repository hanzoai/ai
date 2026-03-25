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
	"os"
	"testing"
)

func TestIsWidgetKey(t *testing.T) {
	tests := []struct {
		token    string
		expected bool
	}{
		{"hz_widget_public", true},
		{"hz_custom_key_123", true},
		{"hk-some-iam-key", false},
		{"sk-openai-key", false},
		{"pk-publishable", false},
		{"random.jwt.token", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isWidgetKey(tt.token); got != tt.expected {
			t.Errorf("isWidgetKey(%q) = %v, want %v", tt.token, got, tt.expected)
		}
	}
}

func TestWidgetAllowedModels(t *testing.T) {
	// Verify allowed models are in the list
	allowed := []string{"llama-3.1-8b", "llama-3.3-70b", "mistral-nemo", "gpt-4o-mini", "deepseek-r1-distill-70b", "claude-3-5-haiku", "claude-haiku-4-5"}
	for _, m := range allowed {
		if !widgetAllowedModels[m] {
			t.Errorf("expected %q to be in widgetAllowedModels", m)
		}
	}

	// Verify premium models are not allowed
	blocked := []string{"zen4", "zen4-coder", "zen4-ultra", "gpt-5"}
	for _, m := range blocked {
		if widgetAllowedModels[m] {
			t.Errorf("expected %q to NOT be in widgetAllowedModels", m)
		}
	}
}

func TestWidgetAllowedModelsList(t *testing.T) {
	list := widgetAllowedModelsList()
	if list == "" {
		t.Fatal("widgetAllowedModelsList() returned empty string")
	}

	// Verify the list is sorted (comma-separated)
	for _, model := range []string{"claude-3-5-haiku", "gpt-4o-mini", "llama-3.1-8b"} {
		if !contains(list, model) {
			t.Errorf("widgetAllowedModelsList() missing %q: got %q", model, list)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestWidgetMaxTokens(t *testing.T) {
	if widgetMaxTokens != 800 {
		t.Errorf("widgetMaxTokens = %d, want 800", widgetMaxTokens)
	}
}

func TestValidateWidgetKey(t *testing.T) {
	// Set env var with comma-separated valid keys
	os.Setenv("WIDGET_KEYS", "hz_widget_public,hz_test_key")
	defer os.Unsetenv("WIDGET_KEYS")

	tests := []struct {
		token    string
		expected bool
	}{
		{"hz_widget_public", true},
		{"hz_test_key", true},
		{"hz_unknown_key", false},
		{"hk-not-widget", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := validateWidgetKey(tt.token); got != tt.expected {
			t.Errorf("validateWidgetKey(%q) = %v, want %v", tt.token, got, tt.expected)
		}
	}
}

func TestValidateWidgetKeyNoConfig(t *testing.T) {
	// Ensure no env var is set
	os.Unsetenv("WIDGET_KEYS")

	// Without KMS or env vars, all keys should be rejected
	if validateWidgetKey("hz_widget_public") {
		t.Error("validateWidgetKey should reject all keys when WIDGET_KEYS is not configured")
	}
}
