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

package controllers

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fast policy: exercise the logic without sleeping through a real backoff.
var testPolicy = retryPolicy{attempts: 3, base: time.Millisecond, max: 2 * time.Millisecond}

func TestIsTransientError_SeparatesWaitFromCascade(t *testing.T) {
	// Waiting fixes these: the provider is busy now, fine in a moment.
	transient := []string{
		"429 Too Many Requests",
		`{"message":"Platform overloaded. Please try again later."}`,
		"503 service unavailable",
		"context deadline exceeded",
	}
	for _, m := range transient {
		if !isTransientError(errors.New(m)) {
			t.Errorf("should be transient (worth waiting for): %q", m)
		}
	}

	// Waiting fixes NONE of these — the provider will never serve this request.
	// They must cascade immediately; sleeping first only burns the deadline.
	permanent := []string{
		"403 this model is not available for your account",
		"401 unauthorized",
		"insufficient_quota",
		"402 payment required",
	}
	for _, m := range permanent {
		err := errors.New(m)
		if isTransientError(err) {
			t.Errorf("must NOT be transient (waiting cannot help): %q", m)
		}
		// ...but they ARE worth trying a different provider for.
		if !isRetryableError(err) {
			t.Errorf("should still cascade to another provider: %q", m)
		}
	}
}

func TestRetryTransient_RidesOutABusyProvider(t *testing.T) {
	// The exact case that sent a 429 to the client before: busy, then fine.
	calls := 0
	err := retryTransient(context.Background(), testPolicy, func() error {
		calls++
		if calls < 3 {
			return errors.New("429 Platform overloaded. Please try again later.")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a provider that recovers must succeed, got %v", err)
	}
	if calls != 3 {
		t.Errorf("want 3 attempts (2 refusals then success), got %d", calls)
	}
}

func TestRetryTransient_DoesNotSleepOnPermanentFailure(t *testing.T) {
	// A 403 must abort on the FIRST attempt: retrying an entitlement error is
	// pure latency, and the client is waiting.
	calls := 0
	start := time.Now()
	slow := retryPolicy{attempts: 5, base: 200 * time.Millisecond, max: time.Second}
	err := retryTransient(context.Background(), slow, func() error {
		calls++
		return errors.New("403 this model is not available for your account")
	})
	if err == nil {
		t.Fatal("expected the 403 to be returned")
	}
	if calls != 1 {
		t.Errorf("a permanent failure must not be retried: %d attempts", calls)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("must not back off on a permanent failure, slept %v", elapsed)
	}
}

func TestRetryTransient_GivesUpAfterAttempts(t *testing.T) {
	calls := 0
	err := retryTransient(context.Background(), testPolicy, func() error {
		calls++
		return errors.New("429 overloaded")
	})
	if err == nil {
		t.Fatal("a provider that never recovers must surface its error")
	}
	if calls != testPolicy.attempts {
		t.Errorf("want exactly %d attempts, got %d", testPolicy.attempts, calls)
	}
}

func TestRetryTransient_StopsWhenClientIsGone(t *testing.T) {
	// Holding a request open is bounded by the caller's deadline: when the
	// client disconnects, we stop waiting instead of pinning a goroutine and a
	// (possibly 250K-token) prompt in memory.
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	slow := retryPolicy{attempts: 5, base: 2 * time.Second, max: 5 * time.Second}

	start := time.Now()
	err := retryTransient(ctx, slow, func() error {
		calls++
		cancel() // client goes away during the first attempt
		return errors.New("429 overloaded")
	})
	if err == nil {
		t.Fatal("expected the last error")
	}
	if calls != 1 {
		t.Errorf("must abandon the request once the client is gone, got %d attempts", calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("must not sleep out the backoff after cancellation, took %v", elapsed)
	}
}

func TestRetryTransient_CannotRetryACommittedStream(t *testing.T) {
	// Once bytes are on the wire the request is not replayable. errPartiallyWritten
	// is deliberately non-transient so this stops at once rather than backing off.
	calls := 0
	err := retryTransient(context.Background(), testPolicy, func() error {
		calls++
		return errPartiallyWritten
	})
	if !errors.Is(err, errPartiallyWritten) {
		t.Fatalf("want errPartiallyWritten, got %v", err)
	}
	if calls != 1 {
		t.Errorf("a partially-written response must never be retried, got %d attempts", calls)
	}
}

func TestRetryPolicy_BackoffGrowsAndIsCapped(t *testing.T) {
	p := retryPolicy{attempts: 6, base: 100 * time.Millisecond, max: 400 * time.Millisecond}
	for n := 0; n < 6; n++ {
		d := p.backoff(n)
		if d <= 0 || d > p.max {
			t.Errorf("backoff(%d)=%v must be in (0, max=%v]", n, d, p.max)
		}
	}
	// Jitter: the wait must not be identical every time, or every client that
	// got a 429 together returns together and re-creates the overload.
	seen := map[time.Duration]bool{}
	for i := 0; i < 40; i++ {
		seen[p.backoff(2)] = true
	}
	if len(seen) == 1 {
		t.Error("backoff must be jittered — an un-jittered retry reassembles the thundering herd")
	}
}
