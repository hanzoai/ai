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

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/beego/beego/logs"
	"github.com/hanzoai/cloud/conf"
)

const (
	crawlStorageDefaultBucket   = "hanzo-crawl"
	crawlStorageDefaultEndpoint = "http://minio.hanzo.svc.cluster.local:9000"
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
	crawlStorageClient *s3.Client
	crawlStorageOnce   sync.Once
)

// getCrawlStorageEndpoint returns the Hanzo Storage endpoint from config.
func getCrawlStorageEndpoint() string {
	endpoint := conf.GetConfigString("crawlStorageEndpoint")
	if endpoint == "" {
		endpoint = crawlStorageDefaultEndpoint
	}
	return endpoint
}

// getCrawlStorageBucket returns the Hanzo Storage bucket name from config.
func getCrawlStorageBucket() string {
	bucket := conf.GetConfigString("crawlStorageBucket")
	if bucket == "" {
		bucket = crawlStorageDefaultBucket
	}
	return bucket
}

// getCrawlStorageClient returns a singleton S3 client configured for Hanzo Storage.
func getCrawlStorageClient() *s3.Client {
	crawlStorageOnce.Do(func() {
		endpoint := getCrawlStorageEndpoint()
		accessKey := conf.GetConfigString("crawlStorageAccessKey")
		secretKey := conf.GetConfigString("crawlStorageSecretKey")
		region := conf.GetConfigString("crawlStorageRegion")
		if region == "" {
			region = "us-east-1"
		}

		cfg := aws.Config{
			Region:      region,
			Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		}

		crawlStorageClient = s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		})
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
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
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

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("Hanzo Storage PutObject failed for %s: %w", key, err)
	}

	logs.Info("crawl archive: stored %d results at %s/%s (%d bytes)", len(results), bucket, key, len(data))
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

	output, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("Hanzo Storage GetObject failed for %s: %w", key, err)
	}
	defer output.Body.Close()

	data, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read crawl archive body for %s: %w", key, err)
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
			logs.Warning("crawl archive: failed to archive results for %s/%s: %v", owner, jobID, err)
		}
	}()
}

// ArchiveCrawlPreviewAsync archives a single-page preview crawl result asynchronously.
// It generates a job ID from the URL and timestamp, then stores both the converted
// ScrapeResult and the raw Crawl4AIResult.
func ArchiveCrawlPreviewAsync(owner, pageURL string, sr ScrapeResult, raw Crawl4AIResult) {
	go func() {
		jobID := hashID(fmt.Sprintf("preview-%s-%s-%d", owner, pageURL, time.Now().UnixNano()))
		if err := ArchiveCrawlResult(owner, jobID, []ScrapeResult{sr}, []Crawl4AIResult{raw}); err != nil {
			logs.Warning("crawl archive: failed to archive preview for %s/%s: %v", owner, pageURL, err)
		}
	}()
}

// IsCrawlStorageConfigured returns true if the crawl storage credentials are set.
func IsCrawlStorageConfigured() bool {
	accessKey := conf.GetConfigString("crawlStorageAccessKey")
	secretKey := conf.GetConfigString("crawlStorageSecretKey")
	return accessKey != "" && secretKey != ""
}
