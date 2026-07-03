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

package object

import "testing"

// TestIsAsyncIngestSource pins the sync/async split: only upload (and the empty
// default) run inline; every bulk/external source runs as a durable workflow.
func TestIsAsyncIngestSource(t *testing.T) {
	cases := map[string]bool{"": false, "upload": false, "github": true, "crawl": true, "s3": true}
	for src, want := range cases {
		if got := IsAsyncIngestSource(src); got != want {
			t.Errorf("IsAsyncIngestSource(%q) = %v, want %v", src, got, want)
		}
	}
}

// TestIngestWorkflowIDDeterministic proves the workflow id (also the idempotency key)
// is a pure function of (owner, store, source, target): the SAME github repo ingest for
// the same org yields the SAME id — so re-submitting a running clone dedups instead of
// launching a duplicate. Different repos / orgs / stores yield DIFFERENT ids.
func TestIngestWorkflowIDDeterministic(t *testing.T) {
	gh := func(repo string) *IngestRequest {
		return &IngestRequest{Store: "docs", Source: "github", GitHub: &GitHubIngestRequest{Repo: repo}}
	}
	a := ingestWorkflowID("hanzo", gh("hanzoai/ai"))
	b := ingestWorkflowID("hanzo", gh("hanzoai/ai"))
	if a != b {
		t.Fatalf("same input must yield same id: %q != %q", a, b)
	}
	if a == ingestWorkflowID("hanzo", gh("hanzoai/cloud")) {
		t.Error("different repo must yield different id")
	}
	if a == ingestWorkflowID("lux", gh("hanzoai/ai")) {
		t.Error("different owner must yield different id (tenant isolation in the id)")
	}
	// The id must be wire-safe (no slashes/spaces from the repo path).
	for _, r := range a {
		if r == '/' || r == ' ' {
			t.Fatalf("workflow id not sanitized: %q", a)
		}
	}
}

// TestIngestWorkflowIDCrawlURL proves a crawl id keys on the URL and stays id-safe
// (scheme/host separators collapsed).
func TestIngestWorkflowIDCrawlURL(t *testing.T) {
	req := &IngestRequest{Store: "kb", Source: "crawl", Crawl: &ScrapeRequest{URL: "https://docs.lux.network/x?a=1"}}
	id := ingestWorkflowID("lux", req)
	for _, bad := range []string{"/", " ", "?", "#", "&", ":"} {
		if containsStr(id, bad) {
			t.Errorf("crawl workflow id %q contains unsafe %q", id, bad)
		}
	}
	if id == ingestWorkflowID("lux", &IngestRequest{Store: "kb", Source: "crawl", Crawl: &ScrapeRequest{URL: "https://other.site"}}) {
		t.Error("different crawl URL must yield different id")
	}
}

// TestEnqueueIngestUnconfigured proves that with no client injected (the composition
// root hasn't wired the tasks engine), EnqueueIngest reports ErrTasksNotConfigured so
// the handler cleanly falls back to inline ingest — ingest is never broken by tasks
// being absent. Also asserts injection is what flips it (SetIngestTasksClient(nil) =
// unconfigured), keeping ai transport-agnostic (no env/dial).
func TestEnqueueIngestUnconfigured(t *testing.T) {
	SetIngestDialer(nil)
	_, err := EnqueueIngest(t.Context(), "unconfigured-org", &IngestRequest{Source: "github", GitHub: &GitHubIngestRequest{Repo: "hanzoai/ai"}}, "en")
	if err != ErrTasksNotConfigured {
		t.Fatalf("want ErrTasksNotConfigured with no dialer, got %v", err)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
