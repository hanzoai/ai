// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

import "testing"

// The config carries a full URL but the SDK wants host and TLS separately, so
// the split is the one place this file can silently send plaintext traffic to a
// TLS endpoint or the reverse.
func TestCrawlStorageEndpoint(t *testing.T) {
	// No crawlStorageEndpoint is configured under test, so the default applies.
	//
	// The default is Hanzo Storage (s3.hanzo.svc), NOT minio: MinIO was retired in
	// favour of hanzoai/s3, and this test was still asserting the old Service —
	// which is exactly the bug main fixed in "crawl storage defaulted to a Service
	// that does not exist". Pin it to the constant so the test cannot drift from
	// the default it is meant to guard.
	host, secure := getCrawlStorageEndpoint()
	if want := "s3.hanzo.svc:9000"; host != want {
		t.Errorf("host = %q, want %q", host, want)
	}
	if secure {
		t.Error("secure = true, want false for the http:// default")
	}
}

func TestCrawlArchiveKey(t *testing.T) {
	if got, want := crawlArchiveKey("acme", "job-1"), "acme/job-1/results.json"; got != want {
		t.Errorf("crawlArchiveKey = %q, want %q", got, want)
	}
}
