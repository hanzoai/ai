// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

// crawl_test.go — proofs for POST /v1/crawl over the native crawl: results come
// back in the order asked and keyed by the url asked for, a page that cannot be
// read degrades to Success:false instead of failing the batch, and a URL aimed at
// the cluster's own internals is REFUSED.
//
// The success path is deliberately NOT faked. apps/crawl refuses every non-public
// address with no env switch and no test hook — which is exactly right for an
// SSRF guard — so a hermetic "it fetched a page" test would have to weaken the
// property it claims to protect. What is asserted here is what can be asserted
// without doing that.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// page serves one HTML document on loopback... which the crawl's guarded dialer
// refuses by design. That refusal is itself the security property under test, so
// this helper exists to prove the refusal, not to fake a public page.
func page(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestCrawlRefusesNonPublicAddresses is the SSRF proof and the whole reason this
// endpoint reads through apps/crawl rather than its own http.Client. A caller
// hands it a URL; if that URL resolves to loopback, link-local, or any other
// non-public address, the fetch is refused rather than performed on the
// cluster's behalf. The crawl4ai dial this replaced had no such guard.
func TestCrawlRefusesNonPublicAddresses(t *testing.T) {
	local := page(t, "<html><head><title>Internal</title></head><body><p>secrets</p></body></html>")

	got, err := Crawl([]string{local, "http://169.254.169.254/latest/meta-data/"})
	if err != nil {
		t.Fatalf("a refused url must not fail the batch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want one result per url, got %d", len(got))
	}
	for i, r := range got {
		if r.Success {
			t.Fatalf("result %d: a non-public address must never be fetched: %+v", i, r)
		}
		if r.Markdown != "" {
			t.Fatalf("result %d: a refused fetch must carry no content, got %q", i, r.Markdown)
		}
		if msg, _ := r.Metadata["error"].(string); msg == "" {
			t.Fatalf("result %d: a refusal must say why, got %+v", i, r.Metadata)
		}
	}
	// The result is keyed by the url that was ASKED for, so a batching caller can
	// still match every result back to its request.
	if got[0].URL != local || got[1].URL != "http://169.254.169.254/latest/meta-data/" {
		t.Fatalf("results must carry the requested url: %+v", got)
	}
}

// TestCrawlPreservesOrder proves the batch contract: results come back in the
// order asked, whatever order the concurrent fetches actually complete in.
func TestCrawlPreservesOrder(t *testing.T) {
	urls := []string{
		"http://127.0.0.1:1/a",
		"http://127.0.0.1:2/b",
		"http://127.0.0.1:3/c",
		"not a url at all",
	}
	got, err := Crawl(urls)
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}
	if len(got) != len(urls) {
		t.Fatalf("want %d results, got %d", len(urls), len(got))
	}
	for i, r := range got {
		if r.URL != urls[i] {
			t.Fatalf("result %d out of order: %q, want %q", i, r.URL, urls[i])
		}
	}
}

// TestCrawlEmptyIsAnError proves the one whole-request failure: being handed
// nothing to do. Every other failure is per-URL and does not fail the batch.
func TestCrawlEmptyIsAnError(t *testing.T) {
	if _, err := Crawl(nil); err == nil {
		t.Fatal("crawling nothing must be an error, not an empty success")
	}
}
