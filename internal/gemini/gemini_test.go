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

package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// rewrite points the client's fixed base URL at a local stub. Swapping the
// transport is the only seam that does not weaken the client's own API.
type rewrite struct{ scheme, host string }

func (rw rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = rw.scheme, rw.host
	return http.DefaultTransport.RoundTrip(r)
}

// stub stands in for generativelanguage.googleapis.com so the request the
// client actually puts on the wire — path, key header, body — is asserted
// against the documented contract without a live key.
func stub(t *testing.T, status int, reply string) (*Client, func() (path, key string, body map[string]any)) {
	t.Helper()
	var path, key string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, key = r.URL.RequestURI(), r.Header.Get("x-goog-api-key")
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := New("test-key", &http.Client{Transport: rewrite{u.Scheme, u.Host}})
	return client, func() (string, string, map[string]any) { return path, key, body }
}

func TestResource(t *testing.T) {
	for in, want := range map[string]string{
		"gemini-2.0-flash":      "models/gemini-2.0-flash",
		"models/gemini-1.5-pro": "models/gemini-1.5-pro",
		"tunedModels/mine":      "tunedModels/mine",
	} {
		if got := resource(in); got != want {
			t.Errorf("resource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCountTokens(t *testing.T) {
	client, seen := stub(t, http.StatusOK, `{"totalTokens":17}`)

	n, err := client.CountTokens(context.Background(), "gemini-2.0-flash",
		[]*Content{Text("hello", RoleUser)})
	if err != nil {
		t.Fatal(err)
	}
	if n != 17 {
		t.Errorf("totalTokens = %d, want 17", n)
	}

	path, key, body := seen()
	if want := "/v1beta/models/gemini-2.0-flash:countTokens"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if key != "test-key" {
		t.Errorf("x-goog-api-key = %q, want test-key", key)
	}
	if _, ok := body["contents"]; !ok {
		t.Errorf("body = %v, want a contents field", body)
	}
}

func TestGenerateContent(t *testing.T) {
	client, seen := stub(t, http.StatusOK,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"},{"text":"there"}]},"tokenCount":5}]}`)

	resp, err := client.GenerateContent(context.Background(), "gemini-2.0-flash",
		[]*Content{Text("hello", RoleUser)})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Candidates[0].TokenCount; got != 5 {
		t.Errorf("tokenCount = %d, want 5", got)
	}
	parts := resp.Candidates[0].Content.Parts
	if len(parts) != 2 || parts[0].Text != "hi" || parts[1].Text != "there" {
		t.Errorf("parts = %+v, want [hi there]", parts)
	}
	if path, _, _ := seen(); path != "/v1beta/models/gemini-2.0-flash:generateContent" {
		t.Errorf("path = %q", path)
	}
}

// A safety block answers 200 with no candidate; that must be an error rather
// than the nil dereference the SDK call site used to risk.
func TestGenerateContentNoCandidate(t *testing.T) {
	client, _ := stub(t, http.StatusOK, `{"candidates":[]}`)

	if _, err := client.GenerateContent(context.Background(), "gemini-2.0-flash",
		[]*Content{Text("hello", RoleUser)}); err == nil {
		t.Fatal("want an error for a candidate-less response, got nil")
	}
}

func TestEmbed(t *testing.T) {
	client, seen := stub(t, http.StatusOK, `{"embeddings":[{"values":[0.5,-0.25]}]}`)

	vectors, err := client.Embed(context.Background(), "text-embedding-004",
		[]*Content{Text("hello", RoleUser)})
	if err != nil {
		t.Fatal(err)
	}
	if want := []float32{0.5, -0.25}; !reflect.DeepEqual(vectors[0], want) {
		t.Errorf("vector = %v, want %v", vectors[0], want)
	}

	path, _, body := seen()
	if want := "/v1beta/models/text-embedding-004:batchEmbedContents"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	requests, ok := body["requests"].([]any)
	if !ok || len(requests) != 1 {
		t.Fatalf("body = %v, want one request", body)
	}
	if got := requests[0].(map[string]any)["model"]; got != "models/text-embedding-004" {
		t.Errorf("request model = %v, want models/text-embedding-004", got)
	}
}

func TestEmbedCountMismatch(t *testing.T) {
	client, _ := stub(t, http.StatusOK, `{"embeddings":[]}`)

	if _, err := client.Embed(context.Background(), "text-embedding-004",
		[]*Content{Text("hello", RoleUser)}); err == nil {
		t.Fatal("want an error when the API returns no embedding, got nil")
	}
}

func TestListModels(t *testing.T) {
	client, seen := stub(t, http.StatusOK,
		`{"models":[{"name":"models/gemini-2.0-flash"}],"nextPageToken":"next"}`)

	page, err := client.ListModels(context.Background(), 50, "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Models) != 1 || page.Models[0].Name != "models/gemini-2.0-flash" {
		t.Errorf("models = %+v", page.Models)
	}
	if page.NextPageToken != "next" {
		t.Errorf("nextPageToken = %q, want next", page.NextPageToken)
	}

	path, _, _ := seen()
	if !strings.HasPrefix(path, "/v1beta/models?") ||
		!strings.Contains(path, "pageSize=50") || !strings.Contains(path, "pageToken=cursor") {
		t.Errorf("path = %q, want /v1beta/models with pageSize and pageToken", path)
	}
}

// The upstream reports bad keys, quota and unknown models only in the body, so
// the body has to survive into the error.
func TestErrorCarriesBody(t *testing.T) {
	client, _ := stub(t, http.StatusTooManyRequests, `{"error":{"message":"quota exceeded"}}`)

	_, err := client.CountTokens(context.Background(), "gemini-2.0-flash",
		[]*Content{Text("hello", RoleUser)})
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("err = %v, want it to carry the upstream message", err)
	}
}
