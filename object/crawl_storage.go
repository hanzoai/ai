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

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/hanzoai/ai/util"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
	hs3 "github.com/hanzos3/go"
	"github.com/hanzos3/go/pkg/credentials"
)

const (
	crawlStorageDefaultBucket = "hanzo-crawl"
	// s3.hanzo.svc is the SeaweedFS S3 gateway, and it is what the other fifteen
	// call sites in the estate already name. The default here was
	// minio.hanzo.svc.cluster.local — a Service that no longer exists, so a
	// deployment that did not override the endpoint had no storage at all.
	crawlStorageDefaultEndpoint = "http://s3.hanzo.svc:9000"
	crawlStorageHTTPTimeout     = 30 * time.Second
)

// CrawlArchive is the envelope stored in Hanzo Storage for a crawl job's results.
// It stores both the converted ScrapeResult data and the raw Crawl4AIResult data
// when available, to preserve the full fidelity of crawl output.
type CrawlArchive struct {
	Owner      string           `json:"owner"`
	JobID      string           `json:"job_id"`
	Timestamp  string           `json:"timestamp"`
	Results    []ScrapeResult   `json:"results"`
	RawResults []Crawl4AIResult `json:"raw_results,omitempty"`
}

var (
	crawlStorageClient *hs3.Client
	crawlStorageOnce   sync.Once
)

// getCrawlStorageEndpoint returns the Hanzo Storage host and whether to reach
// it over TLS. The config carries a full URL but the SDK takes the two apart.
func getCrawlStorageEndpoint() (host string, secure bool) {
	endpoint := conf.GetConfigString("crawlStorageEndpoint")
	if endpoint == "" {
		endpoint = crawlStorageDefaultEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		// A bare "host:port" parses with an empty Host; TLS is the safe default
		// for anything that did not explicitly ask for plaintext.
		return endpoint, true
	}
	return parsed.Host, parsed.Scheme != "http"
}

// getCrawlStorageBucket returns the Hanzo Storage bucket name from config.
func getCrawlStorageBucket() string {
	bucket := conf.GetConfigString("crawlStorageBucket")
	if bucket == "" {
		bucket = crawlStorageDefaultBucket
	}
	return bucket
}

// getCrawlStorageClient returns a singleton Hanzo Storage client.
func getCrawlStorageClient() *hs3.Client {
	crawlStorageOnce.Do(func() {
		host, secure := getCrawlStorageEndpoint()
		region := conf.GetConfigString("crawlStorageRegion")
		if region == "" {
			region = "us-east-1"
		}
		client, err := hs3.New(host, &hs3.Options{
			Creds: credentials.NewStaticV4(
				resolveSecretName("crawlStorageAccessKey"),
				resolveSecretName("crawlStorageSecretKey"), ""),
			Secure: secure,
			Region: region,
			// The endpoint is a service name, never a bucket-addressable host,
			// so buckets have to go in the path.
			BucketLookup: hs3.BucketLookupPath,
		})
		if err != nil {
			log.Error("crawl archive: Hanzo Storage client: %v", err)
			return
		}
		crawlStorageClient = client
	})
	return crawlStorageClient
}

// crawlArchiveKey builds the S3 object key for a crawl job's results.
func crawlArchiveKey(owner, jobID string) string {
	return fmt.Sprintf("%s/%s/results.json", owner, jobID)
}

// ArchiveCrawlResult uploads crawl results as JSON to Hanzo Storage.
// The results are stored at {bucket}/{owner}/{jobID}/results.json.
func ArchiveCrawlResult(owner, jobID string, results []ScrapeResult, rawResults []Crawl4AIResult) error {
	client := getCrawlStorageClient()
	if client == nil {
		return fmt.Errorf("Hanzo Storage client is not initialized")
	}
	archive := CrawlArchive{
		Owner:      owner,
		JobID:      jobID,
		Timestamp:  util.GetCurrentTime(),
		Results:    results,
		RawResults: rawResults,
	}
	data, err := json.Marshal(archive)
	if err != nil {
		return fmt.Errorf("failed to marshal crawl archive: %w", err)
	}
	bucket := getCrawlStorageBucket()
	key := crawlArchiveKey(owner, jobID)
	ctx, cancel := context.WithTimeout(context.Background(), crawlStorageHTTPTimeout)
	defer cancel()
	_, err = client.PutObject(ctx, bucket, key, bytes.NewReader(data), int64(len(data)),
		hs3.PutObjectOptions{ContentType: "application/json"})
	if err != nil {
		return fmt.Errorf("Hanzo Storage PutObject failed for %s: %w", key, err)
	}
	log.Info("crawl archive: stored %d results at %s/%s (%d bytes)", len(results), bucket, key, len(data))
	return nil
}

// GetArchivedCrawlResult retrieves previously archived crawl results from Hanzo Storage.
func GetArchivedCrawlResult(owner, jobID string) (*CrawlArchive, error) {
	client := getCrawlStorageClient()
	if client == nil {
		return nil, fmt.Errorf("Hanzo Storage client is not initialized")
	}
	bucket := getCrawlStorageBucket()
	key := crawlArchiveKey(owner, jobID)
	ctx, cancel := context.WithTimeout(context.Background(), crawlStorageHTTPTimeout)
	defer cancel()
	object, err := client.GetObject(ctx, bucket, key, hs3.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("Hanzo Storage GetObject failed for %s: %w", key, err)
	}
	defer object.Close()
	// The SDK defers the request to the first read, so a missing object or an
	// unreachable endpoint surfaces here rather than above.
	data, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("Hanzo Storage GetObject failed for %s: %w", key, err)
	}
	var archive CrawlArchive
	if err := json.Unmarshal(data, &archive); err != nil {
		return nil, fmt.Errorf("failed to unmarshal crawl archive for %s: %w", key, err)
	}
	return &archive, nil
}

// archiveCrawlResultAsync archives crawl results in a background goroutine.
// Errors are logged but do not propagate to the caller.
func archiveCrawlResultAsync(owner, jobID string, results []ScrapeResult, rawResults []Crawl4AIResult) {
	go func() {
		if err := ArchiveCrawlResult(owner, jobID, results, rawResults); err != nil {
			log.Warning("crawl archive: failed to archive results for %s/%s: %v", owner, jobID, err)
		}
	}()
}

// IsCrawlStorageConfigured returns true if the crawl storage credentials are set.
func IsCrawlStorageConfigured() bool {
	accessKey := resolveSecretName("crawlStorageAccessKey")
	secretKey := resolveSecretName("crawlStorageSecretKey")
	return accessKey != "" && secretKey != ""
}
