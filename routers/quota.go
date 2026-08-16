package routers

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/web"
)

// Usage quotas.
//
// The RateLimiter beside this file is a RATE: a token bucket that refills
// continuously, so a caller who pauses a moment may always send again. It bounds
// how fast, and says nothing about how much.
//
// A quota is a ceiling on how much a caller may spend inside a period, and it
// does not trickle back — it RESETS when the period rolls. The two answer
// different questions and so are kept apart: 60 requests a minute is a rate,
// 6000 requests a month is a quota, and a caller must satisfy both.
//
// Periods are aligned to the clock rather than to a caller's first request. The
// window containing any instant is therefore a pure function of that instant:
// nothing about a window is stored, every caller's window rolls together, and a
// restart cannot hand anyone a fresh allowance by forgetting when theirs began.

// A period is a window that resets as a whole.
type period struct {
	name string
	// window returns the half-open [start, end) of the period containing t.
	window func(t time.Time) (time.Time, time.Time)
}

// The periods a quota is counted over, shortest first, so a refusal names the
// soonest ceiling a caller met.
var periods = []period{
	{"8h", func(t time.Time) (time.Time, time.Time) {
		s := time.Date(t.Year(), t.Month(), t.Day(), t.Hour()/8*8, 0, 0, 0, time.UTC)
		return s, s.Add(8 * time.Hour)
	}},
	{"week", func(t time.Time) (time.Time, time.Time) {
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		s := d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) // back to Monday
		return s, s.AddDate(0, 0, 7)
	}},
	{"month", func(t time.Time) (time.Time, time.Time) {
		s := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		return s, s.AddDate(0, 1, 0)
	}},
}

// What a tier may spend inside each period, in requests, ordered as `periods` is.
// Zero is no ceiling.
//
// These are the numbers to move to change what an account gets; they are the only
// place the amounts are written.
var tierQuotas = map[Tier][]int{
	TierZenFree:       {250, 2000, 6000},
	TierZenPro:        {5000, 40000, 150000},
	TierZenTeam:       {25000, 200000, 750000},
	TierZenEnterprise: {0, 0, 0},
	TierZenCustom:     {0, 0, 0},
}

// spend is one key's count in each period, with the start of the window each
// count belongs to. A count whose window has rolled is stale and reads as zero.
type spend struct {
	since []time.Time
	n     []int
	seen  time.Time
}

// Quota admits requests while a key is inside its tier's ceiling for every period.
type Quota struct {
	mu     sync.Mutex
	keys   map[string]*spend
	tierOf func(apiKey string) Tier
	stopCh chan struct{}

	// Metrics counters — accessed atomically.
	totalAllowed uint64
	totalDenied  uint64
}

// NewQuota creates a Quota that evicts keys untouched for a full month every
// cleanupInterval. The tierFunc callback resolves an API key to its Tier; pass
// nil to always use TierZenFree.
func NewQuota(tierFunc func(string) Tier, cleanupInterval time.Duration) *Quota {
	if tierFunc == nil {
		tierFunc = func(string) Tier { return TierZenFree }
	}
	q := &Quota{
		keys:   make(map[string]*spend),
		tierOf: tierFunc,
		stopCh: make(chan struct{}),
	}
	go q.cleanup(cleanupInterval)
	return q
}

// Spend charges one request to key and reports whether every period admitted it.
//
// A refusal names the period that refused and the instant it next rolls, which is
// what a caller needs to know to come back. A REFUSED request is charged to
// nothing: one exhausted ceiling must not go on burning down the longer ones,
// which would let a caller who is out for eight hours lose their whole month.
func (q *Quota) Spend(key string, now time.Time) (ok bool, refused string, until time.Time) {
	now = now.UTC()
	limits, capped := tierQuotas[q.tierOf(key)]

	q.mu.Lock()
	defer q.mu.Unlock()

	s := q.keys[key]
	if s == nil {
		s = &spend{since: make([]time.Time, len(periods)), n: make([]int, len(periods))}
		q.keys[key] = s
	}
	s.seen = now

	starts := make([]time.Time, len(periods))
	for i, p := range periods {
		start, end := p.window(now)
		starts[i] = start
		if !s.since[i].Equal(start) { // the window rolled: the count resets
			s.since[i] = start
			s.n[i] = 0
		}
		if !capped || i >= len(limits) || limits[i] == 0 {
			continue // no ceiling in this period
		}
		if s.n[i] >= limits[i] {
			atomic.AddUint64(&q.totalDenied, 1)
			return false, p.name, end
		}
	}

	for i := range periods {
		s.since[i] = starts[i]
		s.n[i]++
	}
	atomic.AddUint64(&q.totalAllowed, 1)
	return true, "", time.Time{}
}

// Metrics reports the running allowed and denied counts.
func (q *Quota) Metrics() (allowed, denied uint64) {
	return atomic.LoadUint64(&q.totalAllowed), atomic.LoadUint64(&q.totalDenied)
}

// quotaInstance is the singleton armed by InitRateLimiter.
var quotaInstance *Quota

// charge bills one request to key's quota and refuses it when a period is spent.
//
// It runs only for a request the rate already admitted, so the two ceilings are
// asked in the order a caller meets them: too fast is answered in seconds, too
// much is answered with the instant the period rolls. Retry-After carries that
// instant either way, so one header means one thing.
func charge(ctx *web.Context, key, path string) {
	if quotaInstance == nil {
		return
	}
	ok, period, until := quotaInstance.Spend(key, time.Now())
	if ok {
		return
	}

	retryAfter := int(time.Until(until).Seconds()) + 1
	allowed, denied := quotaInstance.Metrics()
	log.Info("quota_exceeded key=%s path=%s period=%s retry_after=%d total_allowed=%d total_denied=%d",
		maskKey(key), path, period, retryAfter, allowed, denied)

	ctx.ResponseWriter.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
	ctx.ResponseWriter.Header().Set("X-RateLimit-Remaining", "0")
	ctx.ResponseWriter.Header().Set("Content-Type", "application/json")
	ctx.ResponseWriter.WriteHeader(http.StatusTooManyRequests)
	ctx.ResponseWriter.Write([]byte(fmt.Sprintf(
		`{"error":{"message":"Usage limit reached for this %s. It resets in %d seconds.","type":"quota_error","code":429}}`,
		period, retryAfter,
	)))
}

// Stop ends the cleanup goroutine.
func (q *Quota) Stop() { close(q.stopCh) }

// cleanup drops keys nobody has spent against for longer than the longest period,
// after which their counts are all stale anyway.
func (q *Quota) cleanup(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			cut := time.Now().UTC().AddDate(0, -1, 0)
			q.mu.Lock()
			for k, s := range q.keys {
				if s.seen.Before(cut) {
					delete(q.keys, k)
				}
			}
			q.mu.Unlock()
		case <-q.stopCh:
			return
		}
	}
}
