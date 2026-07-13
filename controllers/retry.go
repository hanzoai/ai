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

package controllers

import (
	"context"
	"math/rand"
	"strings"
	"time"
)

// A failing provider fails in one of two ways, and they want opposite handling:
//
//	transient — the provider is busy this second and will serve us the next one
//	            (429 "Platform overloaded", 503, a timeout). Waiting works.
//	permanent — the provider will never serve THIS request, however long we wait
//	            (401, 403 "not available for your account", quota spent).
//	            Waiting is pure latency; the only cure is a different provider.
//
// isRetryableError (failover.go) says "should we cascade?" and answers yes to
// both, which is right: a fallback provider fixes either. But same-provider
// retry is only ever correct for the first kind. Sleeping 15s on a 403 buys
// nothing and burns the client's deadline, so the two are split here.
//
// This is why a 429 must not reach the client. It means "ask again shortly" —
// and we are in a far better position to do that than the client is. The client
// cannot pick a different provider, cannot see the circuit state, and if every
// client retries at once they arrive together and overload the provider again.
// We hold the request, wait out the blip (or route around it), and return the
// answer. A 429 that escapes is a retry loop we pushed onto someone dumber.

// transientSubstrings mark a provider that is momentarily unable to serve, and
// will be able to shortly. Retrying the SAME provider after a short wait is
// worthwhile for these and only these.
var transientSubstrings = []string{
	"429", "rate limit", "too many requests",
	"overloaded", "please try again",
	"500", "internal server error",
	"502", "bad gateway",
	"503", "service unavailable",
	"504", "gateway timeout",
	"timeout", "deadline exceeded",
	"connection reset", "eof",
}

// isTransientError reports whether waiting could plausibly fix this error.
//
// Everything here is also in isRetryableError (a transient failure is, of
// course, worth cascading too) — but the converse does not hold: 401/403/quota
// are retryable-by-cascade and NOT transient, because no amount of waiting will
// change the answer from that provider.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sub := range transientSubstrings {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

// retryPolicy bounds how long we are willing to hold a request open waiting for
// one provider to come back.
//
// Bounded on purpose. "Keep it open until it works" queues an unbounded number
// of in-flight requests — each holding its prompt in memory, which for a 250K
// token body is not free — and turns a provider blip into an outage of our own.
// So: a few attempts, exponential backoff with jitter, and a hard ceiling.
//
// The jitter is load-bearing, not decoration. Without it, every request that
// got a 429 at t sleeps the same interval and returns together at t+delay —
// the same thundering herd that caused the 429, reassembled. Randomizing the
// wait spreads the retries out.
type retryPolicy struct {
	attempts int           // total tries of one provider (1 = no retry)
	base     time.Duration // first backoff
	max      time.Duration // ceiling on any single backoff
}

// defaultRetryPolicy: three tries over roughly 1s + 2s of waiting.
//
// Sized against what we actually measured: DO answers a 429 on the next attempt
// after a short pause, so two extra tries clear the common blip. It is
// deliberately not generous — a provider that is still refusing after a couple
// of seconds is not blipping, it is busy, and the right move then is a
// different provider (the failover chain), not a longer sleep.
//
// This is the DEFAULT, not the policy: models.yaml `retry:` overrides it, so
// the numbers can be tuned (from admin) without a rebuild. See currentRetryPolicy.
var defaultRetryPolicy = retryPolicy{
	attempts: 3,
	base:     500 * time.Millisecond,
	max:      4 * time.Second,
}

// currentRetryPolicy returns the configured policy, falling back to the default
// for any field the config leaves unset. Read per request so a config reload
// takes effect immediately — no restart to change a timeout.
func currentRetryPolicy() retryPolicy {
	p := defaultRetryPolicy

	mc := GetModelConfig()
	if mc == nil {
		return p
	}
	def := mc.RetryConfig()

	if def.Attempts > 0 {
		p.attempts = def.Attempts
	}
	if d, err := time.ParseDuration(def.BaseBackoff); err == nil && d > 0 {
		p.base = d
	}
	if d, err := time.ParseDuration(def.MaxBackoff); err == nil && d > 0 {
		p.max = d
	}
	// A max below base would make backoff() collapse to the cap immediately;
	// keep the invariant rather than trusting the config to be sane.
	if p.max < p.base {
		p.max = p.base
	}
	return p
}

// backoff returns the wait before attempt n (0-indexed): base * 2^n, capped,
// then randomized across [50%, 100%] of that value.
func (p retryPolicy) backoff(n int) time.Duration {
	d := p.base << n // base * 2^n
	if d > p.max || d <= 0 {
		d = p.max
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// waitBeforeRetry sleeps for the backoff, unless the caller's context dies
// first. It reports whether the wait completed — false means the client is
// gone or out of time, and the request must be abandoned rather than retried.
//
// Honouring the context is what keeps "hold the request open" from becoming
// "hold it open forever": we never wait past the deadline the caller gave us.
func (p retryPolicy) waitBeforeRetry(ctx context.Context, n int) bool {
	t := time.NewTimer(p.backoff(n))
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// retryTransient calls attempt until it succeeds, fails permanently, or runs
// out of tries; it returns the final error.
//
// The retry is only safe because it happens BEFORE any byte reaches the client.
// Once a streaming response has started, the request is no longer replayable —
// retrying would either duplicate tokens or corrupt the stream — so callers
// must not use this once output has been flushed. failoverQueryText enforces
// the same rule for cascading (writerHasData), and this obeys it too.
func retryTransient(ctx context.Context, p retryPolicy, attempt func() error) error {
	var err error
	for n := 0; n < p.attempts; n++ {
		if err = attempt(); err == nil {
			return nil
		}
		// A permanent failure will not heal. Stop and let the caller cascade to
		// a provider that can actually serve this request.
		if !isTransientError(err) {
			return err
		}
		if n == p.attempts-1 {
			break // out of tries; do not sleep on the way out
		}
		if !p.waitBeforeRetry(ctx, n) {
			return err // client gone / deadline hit — stop holding the request
		}
	}
	return err
}
