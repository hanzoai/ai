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

package util

import (
	"net/http"
	"testing"
)

func TestOrgFromUserKey(t *testing.T) {
	tests := []struct {
		name    string
		userKey string
		want    string
	}{
		{"owner and name", "owner/name", "owner"},
		{"multi-segment keeps first segment", "hanzo/alice/x", "hanzo"},
		{"no slash -> empty", "noslash", ""},
		{"empty -> empty", "", ""},
		{"surrounding spaces trimmed", " owner / name ", "owner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OrgFromUserKey(tt.userKey); got != tt.want {
				t.Errorf("OrgFromUserKey(%q) = %q, want %q", tt.userKey, got, tt.want)
			}
		})
	}
}

func TestBillingFailOpen(t *testing.T) {
	tests := []struct {
		env  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"yes", true},
		{"on", true},
		{"TRUE", true},  // case-insensitive
		{"On", true},    // case-insensitive
		{" yes ", true}, // trimmed
		{"", false},
		{"0", false},
		{"false", false},
		{"garbage", false},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("BILLING_FAIL_OPEN", tt.env)
			if got := BillingFailOpen(); got != tt.want {
				t.Errorf("BillingFailOpen() with BILLING_FAIL_OPEN=%q = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}

func TestSetCommerceAuthHeaders(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		userKey     string
		wantAuth    string // "" means header must be absent
		wantOrg     string // "" means header must be absent
		authPresent bool
		orgPresent  bool
	}{
		{
			name:        "both populated sets both",
			token:       "svc-token",
			userKey:     "hanzo/alice",
			wantAuth:    "Bearer svc-token",
			wantOrg:     "hanzo",
			authPresent: true,
			orgPresent:  true,
		},
		{
			name:        "empty token omits Authorization",
			token:       "",
			userKey:     "hanzo/alice",
			wantOrg:     "hanzo",
			authPresent: false,
			orgPresent:  true,
		},
		{
			name:        "empty userKey omits X-IAM-Org-Id",
			token:       "svc-token",
			userKey:     "",
			wantAuth:    "Bearer svc-token",
			authPresent: true,
			orgPresent:  false,
		},
		{
			name:        "no-slash userKey omits X-IAM-Org-Id",
			token:       "svc-token",
			userKey:     "noslash",
			wantAuth:    "Bearer svc-token",
			authPresent: true,
			orgPresent:  false,
		},
		{
			name:        "both empty omits both",
			token:       "",
			userKey:     "",
			authPresent: false,
			orgPresent:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://commerce/v1/billing/balance", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			SetCommerceAuthHeaders(req, tt.token, tt.userKey)

			gotAuth := req.Header.Values("Authorization")
			if tt.authPresent {
				if len(gotAuth) != 1 || gotAuth[0] != tt.wantAuth {
					t.Errorf("Authorization = %v, want exactly [%q]", gotAuth, tt.wantAuth)
				}
			} else if len(gotAuth) != 0 {
				t.Errorf("Authorization = %v, want absent", gotAuth)
			}

			gotOrg := req.Header.Values(HeaderIAMOrg)
			if tt.orgPresent {
				if len(gotOrg) != 1 || gotOrg[0] != tt.wantOrg {
					t.Errorf("%s = %v, want exactly [%q]", HeaderIAMOrg, gotOrg, tt.wantOrg)
				}
			} else if len(gotOrg) != 0 {
				t.Errorf("%s = %v, want absent", HeaderIAMOrg, gotOrg)
			}
		})
	}
}
