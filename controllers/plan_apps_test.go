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

package controllers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/internal/authtest"
	"github.com/hanzoai/ai/object"
)

// A token IAM minted for an app names that app. A token delegated for model calls
// names none: its aud is the API it may call, and limited mode — the free model
// answering in place of the one asked for — is for an app's conversation, never for
// a program that named its model (routers' fallback).
func TestADelegatedTokenNamesNoApp(t *testing.T) {
	t.Cleanup(func() { object.SetConfinedVerifier(nil) })
	object.SetConfinedVerifier(func(string) error { return nil })
	key := authtest.Signing(t)
	sign := func(scope, aud string) string {
		s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "https://hanzo.id", "sub": "acme/alice", "owner": "acme", "name": "alice",
			"aud": []string{aud}, "scope": scope,
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		}).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(http.MethodGet, "/apps", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, Apps(c)) })
	for _, tc := range []struct {
		name, token string
		want        []string
	}{
		{"an app's token", sign("openid profile", "hanzo-app"), []string{"hanzo-app"}},
		{"a delegated token", sign("ai:inference", "https://api.hanzo.ai"), nil},
		{"no token", "", nil},
	} {
		req := httptest.NewRequest(http.MethodGet, "/apps", nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		res, err := app.Test(req, zip.TestConfig{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		var got []string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("%s: %s: %v", tc.name, body, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: Apps = %v, want %v", tc.name, got, tc.want)
		}
	}
}
