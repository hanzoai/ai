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
	"strings"
	"testing"
)

// TestGetUserByAccessKeyRequiresClientCreds asserts the IAM service-auth
// requirement: the accessKey lookup must carry clientId+clientSecret, and when
// IAM rejects an unauthenticated service call ({"status":"error"}), the resolver
// returns an ERROR — never a user. (The "don't be fooled" admin-endpoint note.)
func TestGetUserByAccessKeyRequiresClientCreds(t *testing.T) {
	var gotClientID, gotClientSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotClientID = q.Get("clientId")
		gotClientSecret = q.Get("clientSecret")
		w.Header().Set("Content-Type", "application/json")
		if gotClientID == "" || gotClientSecret == "" {
			// IAM's AutoSigninFilter rejects an unauthenticated service call.
			_, _ = w.Write([]byte(`{"status":"error","msg":"Unauthorized operation"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"acme","name":"alice"}}`))
	}))
	defer srv.Close()

	// Creds present → clientId/secret forwarded, user resolved.
	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "the-secret")
	user, err := getUserByAccessKey("hk-acme")
	if err != nil {
		t.Fatalf("with creds: unexpected error %v", err)
	}
	if user == nil || user.Owner != "acme" {
		t.Fatalf("with creds: user=%+v want owner=acme", user)
	}
	if gotClientID != "hanzo-cloud" || gotClientSecret != "the-secret" {
		t.Fatalf("clientId/secret not forwarded: id=%q secret-present=%v", gotClientID, gotClientSecret != "")
	}

	// Creds absent → IAM returns status:error → resolver MUST error, not grant.
	t.Setenv("IAM_CLIENT_ID", "")
	t.Setenv("IAM_CLIENT_SECRET", "")
	user, err = getUserByAccessKey("hk-acme")
	if err == nil {
		t.Fatalf("without creds: expected an error, got user=%+v", user)
	}
	if user != nil {
		t.Fatalf("without creds: must not return a user, got %+v", user)
	}
}

// TestGetUserByAccessKeyRejectsHTML locks the exact regression that broke hk-
// resolution: when the IAM URL is intercepted and serves HTML (the @hanzo/id SPA
// ingress), the JSON decode must fail with an error ("invalid character '<'") —
// the resolver must NOT panic and must NOT fabricate a user. Fail secure.
func TestGetUserByAccessKeyRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>hanzo.id SPA</body></html>`))
	}))
	defer srv.Close()

	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s")
	user, err := getUserByAccessKey("hk-acme")
	if err == nil {
		t.Fatalf("HTML response: expected a decode error, got user=%+v", user)
	}
	if user != nil {
		t.Fatalf("HTML response: must not fabricate a user, got %+v", user)
	}
}

// TestGetUserByAccessKeyNonOKStatus asserts a non-200 from IAM is an error
// (deny), not a silently-empty user.
func TestGetUserByAccessKeyNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "x")
	t.Setenv("IAM_CLIENT_SECRET", "y")
	if _, err := getUserByAccessKey("hk-acme"); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("expected an IAM status error, got %v", err)
	}
}

// TestGetUserByAccessKeyUnknownKeyIsNilUser pins the (nil,nil) contract for an
// unknown key (IAM 200 + data:null). The OpenAI auth path turns this exact
// (nil user, nil err) into a 401 — so the resolver must surface it faithfully.
func TestGetUserByAccessKeyUnknownKeyIsNilUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":null}`))
	}))
	defer srv.Close()
	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_CLIENT_ID", "x")
	t.Setenv("IAM_CLIENT_SECRET", "y")
	user, err := getUserByAccessKey("hk-unknown")
	if err != nil {
		t.Fatalf("unknown key: want (nil,nil), got err=%v", err)
	}
	if user != nil {
		t.Fatalf("unknown key: want nil user (→ 401 upstream), got %+v", user)
	}
}
