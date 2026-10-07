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

package routers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/ai/internal/authtest"
	"github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// A PAYING CALLER IS NEVER READ AS FREE.
//
// The tier used to come from a cache in front of the host's reader that expired
// every five minutes and answered Free for the request that found it empty. A
// payer past Free's eight-hour quota was refused on that one request, and the next
// one, seconds later, was served: an intermittent 429 for a paying customer, once
// per expiry. With a reader installed, every read asks the reader.
func TestAPayingCallerIsNeverReadAsFree(t *testing.T) {
	saved, savedCache := object.TierReader(), tierCache
	t.Cleanup(func() { object.SetTierReader(saved); tierCache = savedCache })
	object.SetTierReader(func(context.Context, string, string) (string, error) { return "pro", nil })
	tierCache = &TierCache{entries: map[string]*tierCacheEntry{}, inflight: map[string]struct{}{}}

	// An entry the old cache would have expired: read as Free on the next call.
	tierCache.entries["acme"] = &tierCacheEntry{tier: TierZenPro, fetchedAt: time.Now().Add(-time.Hour)}

	q := NewQuota(DefaultTierFunc, time.Hour)
	t.Cleanup(q.Stop)
	now := time.Now()
	past := tierQuotas[TierZenFree][0] + 50 // past Free's 8h ceiling, well inside Pro's
	for i := 0; i < past; i++ {
		if ok, period, _ := q.Spend("acme", now); !ok {
			t.Fatalf("request %d of a pro payer refused for its %s — it was read as free", i+1, period)
		}
	}
}

// AN UNREAD TIER IS NOT FREE. When the reader cannot answer, the last tier read
// for the key stands; a key never read is held to Free.
func TestAnUnreadTierKeepsTheLastOneRead(t *testing.T) {
	saved, savedCache := object.TierReader(), tierCache
	t.Cleanup(func() { object.SetTierReader(saved); tierCache = savedCache })
	tierCache = &TierCache{entries: map[string]*tierCacheEntry{}, inflight: map[string]struct{}{}}

	answer := func(context.Context, string, string) (string, error) { return "pro", nil }
	object.SetTierReader(func(ctx context.Context, s, n string) (string, error) { return answer(ctx, s, n) })
	if got := DefaultTierFunc("acme"); got != TierZenPro {
		t.Fatalf("first read = %q, want %q", got, TierZenPro)
	}

	for _, fail := range []func(context.Context, string, string) (string, error){
		func(context.Context, string, string) (string, error) { return "", errors.New("commerce is down") },
		func(context.Context, string, string) (string, error) { return "", nil }, // the reader does not know
	} {
		answer = fail
		if got := DefaultTierFunc("acme"); got != TierZenPro {
			t.Errorf("an unread tier moved the payer to %q, want the last read %q", got, TierZenPro)
		}
		if got := DefaultTierFunc("never-read"); got != TierZenFree {
			t.Errorf("a key never read = %q, want %q", got, TierZenFree)
		}
	}
}

// A STALE ENTRY IS SERVED WHILE IT IS READ AGAIN, on the standalone HTTP route, and
// a refresh that fails replaces nothing.
func TestAStaleTierIsServedAndAFailedRefreshReplacesNothing(t *testing.T) {
	saved, savedCache := object.TierReader(), tierCache
	t.Cleanup(func() { object.SetTierReader(saved); tierCache = savedCache })
	object.SetTierReader(nil)

	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(refused.Close)
	tierCache = &TierCache{
		entries:  map[string]*tierCacheEntry{"acme": {tier: TierZenPro, fetchedAt: time.Now().Add(-time.Hour)}},
		inflight: map[string]struct{}{},
		endpoint: refused.URL,
		client:   refused.Client(),
	}

	if got := DefaultTierFunc("acme"); got != TierZenPro {
		t.Fatalf("a stale pro entry read as %q, want it served while it is read again", got)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		tierCache.inflightMu.Lock()
		n := len(tierCache.inflight)
		tierCache.inflightMu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the refresh never finished")
		}
		time.Sleep(time.Millisecond)
	}
	if got, _, known := tierCache.read("acme"); !known || got != TierZenPro {
		t.Fatalf("after a failed refresh the entry is %q (known %v), want the pro tier it held", got, known)
	}
}

// A CATALOG READ IS NOT USAGE. Listing the models is held to the rate and counted
// against no quota, so a client that lists them before each call does not spend
// its quota twice as fast as it calls them — and a caller whose quota is spent can
// still see what a plan would give them.
func TestTheCatalogIsRateLimitedButNotCharged(t *testing.T) {
	billing(t)
	seen := ceilings(t)
	user := iam.User{Owner: "acme", Name: "alice"}

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range []string{"/v1/models", "/v1/models/", "/v1/models/zen6", "/V1/Models"} {
			before, beforeQuota := seen()
			p := ask(m, path).with("Cookie", "hanzo_iam_token="+authtest.Token(t, user))
			if p.through(RateLimitFilter).status() != http.StatusOK {
				t.Fatalf("%s %s was refused", m, path)
			}
			after, afterQuota := seen()
			if after != before+1 {
				t.Errorf("%s %s met the rate %d time(s), want 1", m, path, after-before)
			}
			if afterQuota != beforeQuota {
				t.Errorf("%s %s was counted against the quota", m, path)
			}
		}
	}

	// A call that asks a model to answer is still counted.
	_, beforeQuota := seen()
	browser(t, user, "/v1/responses").through(RateLimitFilter)
	if _, afterQuota := seen(); afterQuota != beforeQuota+1 {
		t.Fatalf("POST /v1/responses met the quota %d time(s), want 1", afterQuota-beforeQuota)
	}
}
