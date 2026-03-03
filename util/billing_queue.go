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

package util

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/beego/beego/logs"
)

const (
	// billingQueueSize is the capacity of the in-memory usage record buffer.
	// At peak load (~100 req/s), this provides ~160 seconds of headroom.
	billingQueueSize = 16384

	// billingMaxRetries is the maximum number of delivery attempts per record.
	billingMaxRetries = 3

	// billingHTTPTimeout is the per-request timeout for Commerce API calls.
	billingHTTPTimeout = 5 * time.Second

	// billingShutdownTimeout is the maximum time to wait for queue drain on shutdown.
	billingShutdownTimeout = 10 * time.Second

	// billingWorkerCount is the number of concurrent workers draining the queue.
	billingWorkerCount = 4
)

// billingBackoff returns the delay before retry attempt n (0-indexed).
// Sequence: 1s, 4s, 16s (exponential with base 4).
func billingBackoff(attempt int) time.Duration {
	switch attempt {
	case 0:
		return 1 * time.Second
	case 1:
		return 4 * time.Second
	default:
		return 16 * time.Second
	}
}

// BillingRecord holds a pre-serialized usage record ready for HTTP delivery.
// Controllers serialize once; the queue retries with the same payload bytes.
type BillingRecord struct {
	Body      []byte // JSON payload, serialized by the caller
	RequestID string // for structured logging on failure
	User      string // "owner/name" for structured logging
	Model     string // model name for structured logging
}

// BillingQueue is a buffered, retrying usage record delivery queue.
// Records are enqueued without blocking the HTTP handler. Background workers
// drain the queue and POST each record to Commerce with exponential backoff.
type BillingQueue struct {
	endpoint string // Commerce base URL (e.g. "http://commerce:8001")
	token    string // Bearer token for Commerce API
	ch       chan *BillingRecord
	wg       sync.WaitGroup
	stop     chan struct{}
	client   *http.Client
}

// NewBillingQueue creates and starts a billing queue. The endpoint and token
// are resolved once at startup; if the Commerce endpoint is reconfigured at
// runtime the process must be restarted (consistent with how other config
// values are used in cloud).
func NewBillingQueue(endpoint, token string) *BillingQueue {
	q := &BillingQueue{
		endpoint: endpoint,
		token:    token,
		ch:       make(chan *BillingRecord, billingQueueSize),
		stop:     make(chan struct{}),
		client:   &http.Client{Timeout: billingHTTPTimeout},
	}

	q.wg.Add(billingWorkerCount)
	for i := 0; i < billingWorkerCount; i++ {
		go q.worker()
	}

	return q
}

// Enqueue adds a billing record to the delivery queue. If the queue is full,
// the record is dropped and an error is logged. This never blocks the caller.
func (q *BillingQueue) Enqueue(record *BillingRecord) {
	select {
	case q.ch <- record:
	default:
		logs.Error("billing_queue: dropped record user=%s model=%s request_id=%s (queue full)",
			record.User, record.Model, record.RequestID)
	}
}

// Shutdown signals workers to finish and waits for the queue to drain (up to
// billingShutdownTimeout). Returns the number of records that were still
// pending when the timeout expired.
func (q *BillingQueue) Shutdown() int {
	close(q.stop)

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return 0
	case <-time.After(billingShutdownTimeout):
		remaining := len(q.ch)
		logs.Error("billing_queue: shutdown timed out, %d records pending", remaining)
		return remaining
	}
}

// worker drains the queue, delivering each record with retries.
func (q *BillingQueue) worker() {
	defer q.wg.Done()

	for {
		select {
		case record := <-q.ch:
			q.deliver(record)
		case <-q.stop:
			// Drain remaining records before exiting.
			for {
				select {
				case record := <-q.ch:
					q.deliver(record)
				default:
					return
				}
			}
		}
	}
}

// deliver attempts to POST a billing record to Commerce, retrying with
// exponential backoff on transient failures.
func (q *BillingQueue) deliver(record *BillingRecord) {
	url := q.endpoint + "/api/v1/billing/usage"

	for attempt := 0; attempt < billingMaxRetries; attempt++ {
		if attempt > 0 {
			delay := billingBackoff(attempt - 1)

			// Respect shutdown during backoff.
			select {
			case <-time.After(delay):
			case <-q.stop:
				// Still try once more before giving up.
			}
		}

		err := q.post(url, record.Body)
		if err == nil {
			return
		}

		logs.Warning("billing_queue: attempt %d/%d failed user=%s model=%s request_id=%s: %v",
			attempt+1, billingMaxRetries, record.User, record.Model, record.RequestID, err)
	}

	logs.Error("billing_queue: permanently failed user=%s model=%s request_id=%s after %d attempts",
		record.User, record.Model, record.RequestID, billingMaxRetries)
}

// post sends a single HTTP POST to the Commerce billing endpoint.
// Returns nil on 2xx, a retryable error on 5xx/network errors, and a
// non-retryable error on 4xx (which will still be retried — Commerce
// should not return 4xx for valid records, so retrying is safer than dropping).
func (q *BillingQueue) post(url string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if q.token != "" {
		req.Header.Set("Authorization", "Bearer "+q.token)
	}

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	return fmt.Errorf("commerce returned %d", resp.StatusCode)
}
