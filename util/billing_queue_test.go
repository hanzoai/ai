// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package util

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A usage record is the reason an invoice says what it says, so what reaches
// Commerce and what it is scoped to are the contract.
func TestAUsageRecordReachesCommerce(t *testing.T) {
	var mu sync.Mutex
	var gotBody, gotOrg, gotAuth, gotType, gotPath string
	done := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotOrg = string(body), r.Header.Get("X-Org-Id")
		gotAuth, gotType, gotPath = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.URL.Path
		mu.Unlock()
		close(done)
	}))
	defer server.Close()

	q := NewBillingQueue(server.URL, "tok-1")
	q.Enqueue(&BillingRecord{Body: []byte(`{"cents":42}`), Org: "acme", Model: "m", RequestID: "r-1"})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the record never arrived")
	}
	q.Shutdown()

	mu.Lock()
	defer mu.Unlock()
	if gotBody != `{"cents":42}` {
		t.Errorf("body arrived as %q", gotBody)
	}
	// The org scopes the debit to the same balance the credit and the gate use.
	if gotOrg != "acme" {
		t.Errorf("X-Org-Id arrived as %q", gotOrg)
	}
	if gotAuth != "Bearer tok-1" {
		t.Errorf("Authorization arrived as %q", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type arrived as %q", gotType)
	}
	// /v1/ only, never /api/.
	if gotPath != "/v1/billing/usage" {
		t.Errorf("it was posted to %q", gotPath)
	}
}

// One record, one debit: an answer Commerce accepted is not sent again.
func TestAnAcceptedRecordIsNotSentTwice(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	q := NewBillingQueue(server.URL, "")
	q.Enqueue(&BillingRecord{Body: []byte(`{}`), Org: "acme"})
	q.Shutdown()
	time.Sleep(200 * time.Millisecond)

	if n := posts.Load(); n != 1 {
		t.Errorf("Commerce was posted to %d times for one record", n)
	}
}

// A refusal is retried, and a bounded number of times — a record that will never
// land must not hold a worker forever.
func TestARefusedRecordIsRetriedAndThenGivenUpOn(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	q := NewBillingQueue(server.URL, "")
	q.Enqueue(&BillingRecord{Body: []byte(`{}`), Org: "acme"})

	// Shutdown while it is retrying: the backoff yields to it, so this settles in
	// well under the 1s + 4s the schedule would otherwise take.
	time.Sleep(100 * time.Millisecond)
	q.Shutdown()
	time.Sleep(200 * time.Millisecond)

	if n := posts.Load(); n != billingMaxRetries {
		t.Errorf("a refused record was posted %d times, want %d", n, billingMaxRetries)
	}
}

// The buffer is finite. Past it a record is dropped and said to be dropped —
// blocking here would stall the request that produced it.
func TestAFullQueueDropsRatherThanWaits(t *testing.T) {
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer server.Close()

	q := NewBillingQueue(server.URL, "")
	// Deferred calls run last-declared-first, so this releases the workers before
	// Shutdown waits on them — otherwise the wait is the full shutdown timeout.
	defer q.Shutdown()
	defer close(blocked)

	// Fill past capacity; the workers are stuck on the server above.
	settled := make(chan struct{})
	go func() {
		for i := 0; i < billingQueueSize+64; i++ {
			q.Enqueue(&BillingRecord{Body: []byte(`{}`), Org: "acme"})
		}
		close(settled)
	}()
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("Enqueue blocked once the buffer was full; it must drop instead")
	}
}

// The schedule backs off rather than hammering a service that is already unwell.
func TestTheRetryScheduleBacksOff(t *testing.T) {
	last := time.Duration(0)
	for attempt := 0; attempt < 5; attempt++ {
		got := billingBackoff(attempt)
		if got <= 0 {
			t.Fatalf("attempt %d waits %v", attempt, got)
		}
		if got < last {
			t.Errorf("attempt %d waits %v, less than the %v before it", attempt, got, last)
		}
		last = got
	}
}
