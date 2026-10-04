// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	stdcontext "context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/address"
	"github.com/hanzoai/ai/object"
)

// shared stands in for the host's store: counts every limiter instance reads, so a
// new instance — a restart, a second replica — finds what the last one counted.
type shared struct {
	mu     sync.Mutex
	counts map[string]int
	taken  map[string]int
}

func installShared(t *testing.T) *shared {
	t.Helper()
	s := &shared{counts: map[string]int{}, taken: map[string]int{}}
	object.SetCounters(func(_ stdcontext.Context, ws []object.CountWindow) ([]int, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		used := make([]int, len(ws))
		for i, w := range ws {
			used[i] = s.counts[w.Key]
			if used[i] >= w.Limit {
				return used, false
			}
		}
		for i, w := range ws {
			s.counts[w.Key]++
			used[i] = s.counts[w.Key]
		}
		return used, true
	}, func(_ stdcontext.Context, key string, perMin int) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.taken[key] >= perMin {
			return false
		}
		s.taken[key]++
		return true
	})
	t.Cleanup(func() { object.SetCounters(nil, nil) })
	return s
}

// A quota counted on the host's store holds across a restart: a new Quota — the
// process that replaced the last one — refuses where the old one stopped.
func TestAQuotaHoldsAcrossARestart(t *testing.T) {
	installShared(t)
	free := func(string) Tier { return TierZenFree }
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	limit := tierQuotas[TierZenFree][0]
	before := NewQuota(free, time.Hour)
	defer before.Stop()
	for i := 0; i < limit; i++ {
		if ok, _, _ := before.Spend("acme", now); !ok {
			t.Fatalf("request %d of %d refused", i+1, limit)
		}
	}
	after := NewQuota(free, time.Hour) // the restarted process
	defer after.Stop()
	ok, refused, until := after.Spend("acme", now)
	if ok || refused != "8h" || !until.Equal(time.Date(2026, 10, 4, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("after a restart: ok=%v refused=%q until=%v, want the 8h window still spent", ok, refused, until)
	}
}

// The request rate is spent from the host's bucket, so a second limiter — another
// replica — shares the same minute.
func TestTheRateIsOneBucketAcrossReplicas(t *testing.T) {
	installShared(t)
	free := func(string) Tier { return TierZenFree }
	a, b := NewRateLimiter(free, time.Hour), NewRateLimiter(free, time.Hour)
	defer a.Stop()
	defer b.Stop()
	lanes := []address.Bucket{{Key: "acme", Scale: 1}}
	admitted := 0
	for i := 0; i < 2*tierLimits[TierZenFree]; i++ {
		l := a
		if i%2 == 1 {
			l = b
		}
		if l.Admit(lanes) {
			admitted++
		}
	}
	if admitted != tierLimits[TierZenFree] {
		t.Fatalf("two replicas admitted %d against one minute of %d", admitted, tierLimits[TierZenFree])
	}
}
