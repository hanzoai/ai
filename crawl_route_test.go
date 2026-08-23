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
	"testing"

	"github.com/hanzoai/ai/routers"
	"github.com/zap-proto/zip"
)

// WHAT THIS SERVICE SERVES IS THE ROUTE TABLE, and these two ask it directly.
//
// They used to ask by sending a request and reading the status — 404 meant absent,
// an auth refusal meant present. That stopped distinguishing anything the day the
// filter chain closed by default: an unnamed /v1 address is refused BEFORE the
// router gets to look for it, so a registered endpoint and an absent one now answer
// the same 401. Absence became untestable that way and presence became free, which
// is the worse half — the search assertion below would have passed with the route
// deleted.
//
// The table is the thing both were reaching for anyway, and it says which it is.

// serves reports whether the app this process builds answers at pattern.
func serves(t *testing.T, pattern string) bool {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	routes(app)
	table := routers.Patterns(app)
	if len(table) == 0 {
		t.Fatal("no routes registered; the instrument is broken, not the routing")
	}
	_, ok := table[pattern]
	return ok
}

// THIS SERVICE SERVES NO /v1/crawl, and that is the assertion.
//
// ai does not fetch the web: object.SetFetcher takes the crawl from whoever mounted
// it, and nothing in this module ever calls it. An endpoint here could therefore
// answer nothing on its own — and where a host IS present, that host serves the same
// address with its own typed operation, so registering it made one address the
// property of two apps, which a fleet cannot route and a document must not pick a
// winner for.
//
// The crawl ai does offer is the one its host feeds, over ZAP
// (controllers/zap_rag-search-crawl.go). That is a different address and it stays.
func TestNoCrawlDoorHere(t *testing.T) {
	if serves(t, "/v1/crawl") {
		t.Fatal("/v1/crawl is registered — this service put an endpoint onto a crawl it does not have")
	}
}

func TestSearchRouteStillRegistered(t *testing.T) {
	if !serves(t, "/v1/search") {
		t.Fatal("/v1/search is not registered — the native search route was lost")
	}
}
