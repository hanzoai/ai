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

package ai

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// THIS SERVICE SERVES NO /v1/crawl, and that is the assertion.
//
// ai does not fetch the web: object.SetFetcher takes the crawl from whoever mounted
// it, and nothing in this module ever calls it. A door here could therefore answer
// nothing on its own — and where a host IS present, that host serves the same address
// with its own typed operation, so registering it made one address the property of
// two apps, which a fleet cannot route and a document must not pick a winner for.
//
// The crawl ai does offer is the one its host feeds, over ZAP
// (controllers/zap_rag-search-crawl.go). That is a different door and it stays.
func TestNoCrawlDoorHere(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	routes(app)

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/v1/crawl",
		strings.NewReader(`{"url":"https://example.com"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /v1/crawl answered %d — this service registered a door onto a crawl it does not have. body=%q",
			resp.StatusCode, string(body))
	}
}

func TestSearchRouteStillRegistered(t *testing.T) {

	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	routes(app)

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/v1/search",
		strings.NewReader(`{"query":"hello"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)

	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("POST /v1/search: 404 — native search route lost. body=%q", bs)
	}
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("POST /v1/search: 500 (panic). body=%q", bs)
	}
	if !strings.Contains(bs, "authentication required") {
		t.Fatalf("POST /v1/search: expected fail-closed auth rejection, got %q", bs)
	}
}
