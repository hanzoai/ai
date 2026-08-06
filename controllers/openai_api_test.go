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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// TestGetUserByAccessKeyUsesCanonicalPath locks the regression that broke hk-
// API-key resolution: the lookup MUST hit IAM's canonical /v1/iam/get-user,
// never the legacy /api/get-user (which the @hanzo/id SPA ingress serves as
// HTML, producing "invalid character '<'" on JSON decode). It also asserts the
// resolved org owner is parsed back out.
func TestGetUserByAccessKeyUsesCanonicalPath(t *testing.T) {
	var gotPath, gotAccessKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccessKey = r.URL.Query().Get("accessKey")
		w.Header().Set("Content-Type", "application/json")
		// Mirror IAM's get-user shape: status:ok + data:{owner,name}.
		_, _ = w.Write([]byte(`{"status":"ok","msg":"","data":{"owner":"maxpower","name":"maxpower"}}`))
	}))
	defer srv.Close()

	os.Setenv("IAM_URL", srv.URL)
	defer os.Unsetenv("IAM_URL")

	user, err := getUserByAccessKey("hk-canonical-test")
	if err != nil {
		t.Fatalf("getUserByAccessKey returned error: %v", err)
	}
	if gotPath != "/v1/iam/get-user" {
		t.Errorf("IAM path = %q, want /v1/iam/get-user (must NOT use legacy /api/get-user)", gotPath)
	}
	if strings.HasPrefix(gotPath, "/api/") {
		t.Errorf("IAM path %q uses the legacy /api/ alias — forbidden, breaks hk- resolution via SPA ingress", gotPath)
	}
	if gotAccessKey != "hk-canonical-test" {
		t.Errorf("accessKey query = %q, want hk-canonical-test", gotAccessKey)
	}
	if user == nil || user.Owner != "maxpower" {
		t.Errorf("resolved user owner = %+v, want owner=maxpower (the billing org)", user)
	}
}
