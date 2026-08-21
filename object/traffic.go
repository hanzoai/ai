// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Live request-geo aggregate for the world.hanzo.ai "Hanzo mode" globe. It answers
// exactly one question — WHERE is api.hanzo.ai traffic coming from, right now — and
// answers it with AGGREGATES ONLY.
//
// PRIVACY INVARIANT (load-bearing, do not weaken): this aggregate stores only a
// counter keyed by (country, subdivision, service-class). It NEVER stores a client
// IP, NEVER hashes an IP, NEVER stores a per-request row, and carries NO org or user
// dimension. Record's signature is the proof: it takes a country code, a region
// code, and a service class — there is nowhere to put an IP. Geo comes from the edge
// (Cloudflare CF-IPCountry / CF-Region-Code); CF-Connecting-IP is never read. This
// is why the /v1/ai/traffic/globe endpoint is safe to serve publicly, unauthenticated.
//
// MEMORY BOUND: a fixed ring of TrafficBuckets minute-buckets (24h). A slot is
// reused every TrafficBuckets minutes and reset on reuse, so memory is
// O(buckets × distinct (country,region,service)) — country×service cardinality is
// naturally ~250×5, and trafficMaxKeysPerBucket hard-caps a pathological minute.
// Zero DB writes on the hot path: Record is an O(1) map bump under a short lock.

const (
	// TrafficBuckets is the ring size: one bucket per minute for a rolling 24h. It
	// bounds both memory and the maximum queryable window.
	TrafficBuckets = 24 * 60
	// TrafficMaxWindowMinutes caps the Globe query window to the ring size.
	TrafficMaxWindowMinutes = TrafficBuckets
	// TrafficDefaultWindowMinutes is the default Globe window — the last hour.
	TrafficDefaultWindowMinutes = 60
	// trafficMaxKeysPerBucket hard-bounds one minute's key cardinality so spoofed or
	// adversarial header noise can never grow a bucket without limit. Real
	// country×service cardinality sits far below this; the cap only trims abuse.
	trafficMaxKeysPerBucket = 8192
	// trafficTopCountries is how many countries the public totals rank.
	trafficTopCountries = 10
)

// Service classes — the coarse, path-derived dimension. Kept tiny and stable so the
// public aggregate reveals product shape (chat vs media vs embeddings) without ever
// leaking concrete endpoint structure.
const (
	SvcChat       = "chat"
	SvcModels     = "models"
	SvcEmbeddings = "embeddings"
	SvcMedia      = "media"
	SvcOther      = "other"
)

// TrafficPoint is one plotted globe point: a (country[, region]) group at a centroid
// with its request count and per-service-class breakdown over the window.
type TrafficPoint struct {
	Country   string         `json:"country"`
	Region    string         `json:"region,omitempty"`
	Lat       float64        `json:"lat"`
	Lon       float64        `json:"lon"`
	Count     int            `json:"count"`
	ByService map[string]int `json:"byService"`
	// ByTask is the router task-classification mix (code/reasoning/chat/vision/…) for
	// this region — "what are customers here DOING." A SUBSET of Count (only classified
	// chat requests carry a task). Omitted when nothing was classified (honest empty).
	ByTask map[string]int `json:"byTask,omitempty"`
}

// TrafficCountryCount is one country's total in the ranked top-countries list.
type TrafficCountryCount struct {
	Country string `json:"country"`
	Count   int    `json:"count"`
}

// TrafficTotals is the headline throughput surface for the "live throughput" panel.
// RPS1m is requests/second over the last complete minute (stable, honest — the
// in-progress minute is excluded so the number never dips mid-minute); RPM60m is the
// average requests/minute over the trailing hour; TopCountries ranks the window's
// resolvable countries by count.
type TrafficTotals struct {
	RPS1m        float64               `json:"rps_1m"`
	RPM60m       float64               `json:"rpm_60m"`
	TopCountries []TrafficCountryCount `json:"top_countries"`
}

// TrafficWindow describes the resolved aggregation window.
type TrafficWindow struct {
	Minutes int    `json:"minutes"`
	Since   string `json:"since"`
	Until   string `json:"until"`
}

// TrafficGlobe is the whole public payload: window + plotted points + throughput.
type TrafficGlobe struct {
	Window TrafficWindow  `json:"window"`
	Points []TrafficPoint `json:"points"`
	Totals TrafficTotals  `json:"totals"`
}

// geoKey groups counts by (country, region). There is no IP here, by construction.
type geoKey struct {
	country string // ISO-3166 alpha-2, upper; "" only when the edge gave no usable code
	region  string // ISO-3166-2 subdivision (e.g. "CA"); "" when absent
}

// geoCount is one geo group's counts within a minute: total requests (count) plus two
// INDEPENDENT breakdowns — byService (path class, from the edge tap, present on EVERY
// request) and byTask (router task classification, from the routing decision, present
// only on classified chat requests, so a subset of count).
type geoCount struct {
	count     int
	byService map[string]int
	byTask    map[string]int
}

// trafficBucket is one minute. minute is the unix-minute index this slot currently
// holds (-1 = never written); total counts EVERY recorded request in the minute
// (including geo-unresolvable ones, so the throughput rate is honest); geo holds only
// geo-resolvable groups (what the globe plots).
type trafficBucket struct {
	minute int64
	total  int
	geo    map[geoKey]*geoCount
}

// roll reclaims the slot for minute m when it currently holds a different (or empty)
// minute — the ring rotation that bounds memory (a stale minute's maps are dropped).
func (b *trafficBucket) roll(m int64) {
	if b.minute != m {
		b.minute = m
		b.total = 0
		b.geo = make(map[geoKey]*geoCount)
	}
}

// ensureGeo returns the group for k, creating it (with empty breakdowns) unless the
// per-minute cardinality cap is hit — a defensive bound real traffic never reaches.
// Returns nil only when the cap blocks a NEW group.
func (b *trafficBucket) ensureGeo(k geoKey) *geoCount {
	g := b.geo[k]
	if g == nil {
		if len(b.geo) >= trafficMaxKeysPerBucket {
			return nil
		}
		g = &geoCount{byService: map[string]int{}, byTask: map[string]int{}}
		b.geo[k] = g
	}
	return g
}

// TrafficAggregator is the in-process minute-ring. Safe for concurrent Record/Globe.
type TrafficAggregator struct {
	mu      sync.Mutex
	buckets []trafficBucket
}

// NewTrafficAggregator returns an empty ring.
func NewTrafficAggregator() *TrafficAggregator {
	b := make([]trafficBucket, TrafficBuckets)
	for i := range b {
		b[i].minute = -1
	}
	return &TrafficAggregator{buckets: b}
}

// GlobalTraffic is the single process-wide aggregate. Like GlobalBalanceLedger it is
// in-pod memory — correct at the enforced cloud-api replicas:1 topology, and a
// best-effort marketing aggregate regardless (it stores only counts, so there is no
// exactly-once state to coordinate across pods).
var GlobalTraffic = NewTrafficAggregator()

// Record folds ONE request into the current minute. It takes only the edge geo codes
// and a service class — there is deliberately no parameter for an IP or any
// per-request identity. O(1), never blocks, never errors.
func (a *TrafficAggregator) Record(country, region, service string) {
	a.recordAt(country, region, service, time.Now())
}

// recordAt is Record with an injectable clock (tests drive bucket rotation with it).
func (a *TrafficAggregator) recordAt(country, region, service string, now time.Time) {
	country = normCountry(country)
	region = normRegion(region)
	if service == "" {
		service = SvcOther
	}
	m := now.Unix() / 60
	idx := int(m % int64(TrafficBuckets))

	a.mu.Lock()
	defer a.mu.Unlock()
	b := &a.buckets[idx]
	b.roll(m)
	b.total++
	if country == "" {
		return // counted in the honest total, but not plottable — no centroid.
	}
	g := b.ensureGeo(geoKey{country: country, region: region})
	if g == nil {
		return // defensive cardinality cap; real traffic never reaches it.
	}
	g.count++
	g.byService[service]++
}

// RecordTask folds ONE classified request's TASK into its geo group's byTask
// breakdown — the router's task classification (code/reasoning/chat/…) tagged with
// the request's edge country/region, so the globe can answer "what are customers in
// each region DOING." Like Record it takes only geo codes + a label — NO IP. It
// enriches an EXISTING geo group only (the edge tap already counted the request under
// byService microseconds earlier); it never creates a group or touches count/total, so
// byTask stays a subset of count and never invents a plotted point. O(1), best-effort.
func (a *TrafficAggregator) RecordTask(country, region, task string) {
	a.recordTaskAt(country, region, task, time.Now())
}

// recordTaskAt is RecordTask with an injectable clock.
func (a *TrafficAggregator) recordTaskAt(country, region, task string, now time.Time) {
	country = normCountry(country)
	task = normTask(task)
	if country == "" || task == "" {
		return
	}
	region = normRegion(region)
	m := now.Unix() / 60
	idx := int(m % int64(TrafficBuckets))

	a.mu.Lock()
	defer a.mu.Unlock()
	b := &a.buckets[idx]
	b.roll(m)
	// Enrich only an existing group; if the minute has rolled since the tap counted
	// this request, drop the task (a bounded, honest best-effort loss).
	g := b.geo[geoKey{country: country, region: region}]
	if g == nil {
		return
	}
	g.byTask[task]++
}

// Globe folds the ring over the trailing windowMin minutes into the public payload.
// now is injectable for tests. Pure over the ring snapshot it takes under the lock.
func (a *TrafficAggregator) Globe(windowMin int, now time.Time) TrafficGlobe {
	if windowMin <= 0 {
		windowMin = TrafficDefaultWindowMinutes
	}
	if windowMin > TrafficMaxWindowMinutes {
		windowMin = TrafficMaxWindowMinutes
	}
	curMinute := now.Unix() / 60
	oldest := curMinute - int64(windowMin) + 1

	type groupAgg struct {
		count     int
		byService map[string]int
		byTask    map[string]int
	}
	groups := map[geoKey]*groupAgg{}
	countryTotals := map[string]int{}
	var last1m, last60m int

	a.mu.Lock()
	for i := range a.buckets {
		b := &a.buckets[i]
		if b.minute < oldest || b.minute > curMinute {
			continue
		}
		if b.minute == curMinute-1 { // last COMPLETE minute → the stable rps basis
			last1m += b.total
		}
		if b.minute >= curMinute-59 { // trailing 60 minutes → the rpm basis
			last60m += b.total
		}
		for k, gc := range b.geo {
			g := groups[k]
			if g == nil {
				g = &groupAgg{byService: map[string]int{}, byTask: map[string]int{}}
				groups[k] = g
			}
			g.count += gc.count
			for s, n := range gc.byService {
				g.byService[s] += n
			}
			for t, n := range gc.byTask {
				g.byTask[t] += n
			}
			countryTotals[k.country] += gc.count
		}
	}
	a.mu.Unlock()

	points := make([]TrafficPoint, 0, len(groups))
	for gk, g := range groups {
		lat, lon, ok := centroid(gk.country, gk.region)
		if !ok {
			continue
		}
		points = append(points, TrafficPoint{
			Country:   gk.country,
			Region:    gk.region,
			Lat:       lat,
			Lon:       lon,
			Count:     g.count,
			ByService: g.byService,
			ByTask:    g.byTask, // omitempty drops it when no task was classified
		})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].Count != points[j].Count {
			return points[i].Count > points[j].Count
		}
		if points[i].Country != points[j].Country {
			return points[i].Country < points[j].Country
		}
		return points[i].Region < points[j].Region
	})

	until := time.Unix(curMinute*60, 0).UTC()
	since := time.Unix(oldest*60, 0).UTC()
	return TrafficGlobe{
		Window: TrafficWindow{
			Minutes: windowMin,
			Since:   since.Format(time.RFC3339),
			Until:   until.Format(time.RFC3339),
		},
		Points: points,
		Totals: TrafficTotals{
			RPS1m:        round2(float64(last1m) / 60.0),
			RPM60m:       round2(float64(last60m) / 60.0),
			TopCountries: topCountries(countryTotals),
		},
	}
}

// topCountries ranks a country→count map by count desc (ties by code) and trims.
func topCountries(m map[string]int) []TrafficCountryCount {
	out := make([]TrafficCountryCount, 0, len(m))
	for c, v := range m {
		out = append(out, TrafficCountryCount{Country: c, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Country < out[j].Country
	})
	if len(out) > trafficTopCountries {
		out = out[:trafficTopCountries]
	}
	return out
}

// TrafficServiceClass maps a request path to its coarse service class. Pure, so the
// tap filter and the tests classify identically.
func TrafficServiceClass(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/chat/"),
		path == "/v1/messages", strings.HasPrefix(path, "/v1/messages/"),
		strings.HasPrefix(path, "/v1/completions"),
		strings.HasPrefix(path, "/v1/responses"):
		return SvcChat
	case path == "/v1/models", strings.HasPrefix(path, "/v1/models/"):
		return SvcModels
	case strings.HasPrefix(path, "/v1/embeddings"):
		return SvcEmbeddings
	case strings.HasPrefix(path, "/v1/images"),
		strings.HasPrefix(path, "/v1/videos"),
		strings.HasPrefix(path, "/v1/audio"),
		strings.HasPrefix(path, "/v1/generate-text-to-speech"),
		strings.HasPrefix(path, "/v1/process-speech-to-text"):
		return SvcMedia
	default:
		return SvcOther
	}
}

// TrafficShouldRecord selects which requests count toward the aggregate: genuine
// inbound /v1 API calls only. OPTIONS preflights, health/metrics probes, and the
// globe poll itself (world.hanzo.ai hits /v1/ai/traffic/globe every ~12s) are excluded
// so the marketing rate reflects real product traffic, not self-noise.
func TrafficShouldRecord(path, method string) bool {
	if method == "OPTIONS" {
		return false
	}
	if !strings.HasPrefix(path, "/v1/") {
		return false
	}
	switch {
	case path == "/v1/health", path == "/v1/metrics":
		return false
	case strings.HasPrefix(path, "/v1/ai/traffic/"):
		return false
	}
	return true
}

// normCountry uppercases and validates an ISO-3166 alpha-2 code. Cloudflare's
// non-country sentinels (XX unknown, T1 Tor) and any malformed value collapse to ""
// — counted in the total, dropped from the plotted points.
func normCountry(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 2 {
		return ""
	}
	switch s {
	case "XX", "T1", "ZZ":
		return ""
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return ""
		}
	}
	return s
}

// normRegion uppercases and validates a short subdivision code; anything else → "".
func normRegion(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) == 0 || len(s) > 3 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return ""
		}
	}
	return s
}

// normTask lowercases and validates a router task label (code/reasoning/vision/…):
// short, [a-z_] only; anything else → "" (dropped). The task set is controlled (the
// router.Task constants), so this is a light defensive sanitizer, not a whitelist.
func normTask(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) == 0 || len(s) > 20 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || c == '_') {
			return ""
		}
	}
	return s
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
