// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

// minTime returns a deterministic instant 1s into the given unix-minute index, so
// tests can place records in exact minutes and drive ring rotation by the clock.
func minTime(m int64) time.Time { return time.Unix(m*60+1, 0).UTC() }

const testMinute int64 = 28_000_000 // an arbitrary, stable minute index

func TestTrafficServiceClass(t *testing.T) {
	cases := map[string]string{
		"/v1/chat/completions":              SvcChat,
		"/v1/messages":                      SvcChat,
		"/v1/messages/abc":                  SvcChat,
		"/v1/completions":                   SvcChat,
		"/v1/responses":                     SvcChat,
		"/v1/models":                        SvcModels,
		"/v1/models/zen-omni-30b":           SvcModels,
		"/v1/embeddings":                    SvcEmbeddings,
		"/v1/images/generations":            SvcMedia,
		"/v1/videos/generations":            SvcMedia,
		"/v1/audio/speech":                  SvcMedia,
		"/v1/generate-text-to-speech-audio": SvcMedia,
		"/v1/process-speech-to-text":        SvcMedia,
		"/v1/ai/rag/query":                  SvcOther,
		"/v1/get-account":                   SvcOther,
	}
	for path, want := range cases {
		if got := TrafficServiceClass(path); got != want {
			t.Errorf("TrafficServiceClass(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestTrafficShouldRecord(t *testing.T) {
	yes := []struct{ path, method string }{
		{"/v1/chat/completions", "POST"},
		{"/v1/models", "GET"},
		{"/v1/embeddings", "POST"},
	}
	for _, c := range yes {
		if !TrafficShouldRecord(c.path, c.method) {
			t.Errorf("TrafficShouldRecord(%q,%q) = false, want true", c.path, c.method)
		}
	}
	no := []struct{ path, method string }{
		{"/v1/chat/completions", "OPTIONS"}, // preflight
		{"/v1/health", "GET"},               // probe
		{"/v1/metrics", "GET"},              // probe
		{"/v1/ai/traffic/globe", "GET"},     // the poll itself must not self-count
		{"/healthz", "GET"},                 // non-/v1
		{"/", "GET"},                        // non-/v1
	}
	for _, c := range no {
		if TrafficShouldRecord(c.path, c.method) {
			t.Errorf("TrafficShouldRecord(%q,%q) = true, want false", c.path, c.method)
		}
	}
}

func TestNormCountryAndRegion(t *testing.T) {
	country := map[string]string{
		"us": "US", " gb ": "GB", "US": "US",
		"XX": "", "T1": "", "ZZ": "", "": "", "USA": "", "u1": "",
	}
	for in, want := range country {
		if got := normCountry(in); got != want {
			t.Errorf("normCountry(%q) = %q, want %q", in, got, want)
		}
	}
	region := map[string]string{
		"ca": "CA", "CA": "CA", "13": "13", "": "", "toolong": "", "a!": "",
	}
	for in, want := range region {
		if got := normRegion(in); got != want {
			t.Errorf("normRegion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRecordAndGlobe covers the core aggregation: grouping by (country,region),
// per-service breakdown, throughput rates (rps from the last complete minute; rpm
// over the hour), ranked top countries, deterministic point ordering, and the
// centroid resolution (US-CA subdivision vs country fallback).
func TestRecordAndGlobe(t *testing.T) {
	a := NewTrafficAggregator()
	m := testMinute
	// Record in the PREVIOUS complete minute so rps_1m (curMinute-1) is non-zero.
	a.recordAt("US", "CA", SvcChat, minTime(m-1))
	a.recordAt("US", "CA", SvcChat, minTime(m-1))
	a.recordAt("US", "", SvcModels, minTime(m-1))
	a.recordAt("GB", "", SvcChat, minTime(m-1))
	a.recordAt("XX", "", SvcOther, minTime(m-1)) // unknown country: total only, no point

	g := a.Globe(60, minTime(m))

	if g.Window.Minutes != 60 {
		t.Fatalf("window minutes = %d, want 60", g.Window.Minutes)
	}
	// 5 requests total in the last complete minute → rps = 5/60, rpm = 5/60.
	if g.Totals.RPS1m != round2(5.0/60.0) {
		t.Errorf("rps_1m = %v, want %v", g.Totals.RPS1m, round2(5.0/60.0))
	}
	if g.Totals.RPM60m != round2(5.0/60.0) {
		t.Errorf("rpm_60m = %v, want %v", g.Totals.RPM60m, round2(5.0/60.0))
	}
	// top_countries: US=3 (2 CA-chat + 1 models), GB=1.
	if len(g.Totals.TopCountries) != 2 ||
		g.Totals.TopCountries[0] != (TrafficCountryCount{Country: "US", Count: 3}) ||
		g.Totals.TopCountries[1] != (TrafficCountryCount{Country: "GB", Count: 1}) {
		t.Fatalf("top_countries = %+v, want [US:3 GB:1]", g.Totals.TopCountries)
	}
	// 3 plotted points; XX dropped (no centroid). Order: count desc, then country
	// asc, then region asc → (US,CA)#2, (GB,"")#1, (US,"")#1.
	if len(g.Points) != 3 {
		t.Fatalf("points = %d, want 3 (%+v)", len(g.Points), g.Points)
	}
	p0 := g.Points[0]
	if p0.Country != "US" || p0.Region != "CA" || p0.Count != 2 || p0.ByService[SvcChat] != 2 {
		t.Errorf("point[0] = %+v, want US/CA count2 chat2", p0)
	}
	// US-CA must resolve to the state centroid, NOT the country centroid.
	if usca := subdivisionCentroid["US-CA"]; p0.Lat != usca[0] || p0.Lon != usca[1] {
		t.Errorf("US-CA centroid = (%v,%v), want %v", p0.Lat, p0.Lon, usca)
	}
	if g.Points[1].Country != "GB" || g.Points[1].Count != 1 {
		t.Errorf("point[1] = %+v, want GB count1", g.Points[1])
	}
	p2 := g.Points[2]
	if p2.Country != "US" || p2.Region != "" || p2.ByService[SvcModels] != 1 {
		t.Errorf("point[2] = %+v, want US/'' models1", p2)
	}
	// The regionless US point falls back to the country centroid.
	if usc := countryCentroid["US"]; p2.Lat != usc[0] || p2.Lon != usc[1] {
		t.Errorf("US country centroid = (%v,%v), want %v", p2.Lat, p2.Lon, usc)
	}
}

// TestBucketRotation proves the ring RESETS a slot on reuse: recording into the same
// slot TrafficBuckets minutes later must not carry the old minute's counts. Without
// the reset, the stale US count would still be in the (now current-minute) bucket
// and would leak into the aggregate.
func TestBucketRotation(t *testing.T) {
	a := NewTrafficAggregator()
	m := testMinute
	a.recordAt("US", "", SvcChat, minTime(m))                // slot idx = m % buckets
	a.recordAt("GB", "", SvcChat, minTime(m+TrafficBuckets)) // same slot, one ring later

	g := a.Globe(60, minTime(m+TrafficBuckets))
	if len(g.Points) != 1 || g.Points[0].Country != "GB" {
		t.Fatalf("after ring reuse, points = %+v, want exactly [GB]", g.Points)
	}
	if g.Totals.RPM60m == 0 {
		t.Error("GB record should count toward the trailing-hour rate")
	}
}

// TestWindowFiltering: records outside the requested window are excluded; widening
// the window brings them back.
func TestWindowFiltering(t *testing.T) {
	a := NewTrafficAggregator()
	m := testMinute
	a.recordAt("US", "", SvcChat, minTime(m-120)) // 2h ago
	a.recordAt("GB", "", SvcChat, minTime(m-1))   // 1m ago

	if g := a.Globe(60, minTime(m)); len(g.Points) != 1 || g.Points[0].Country != "GB" {
		t.Fatalf("60m window points = %+v, want [GB]", g.Points)
	}
	if g := a.Globe(180, minTime(m)); len(g.Points) != 2 {
		t.Fatalf("180m window points = %d, want 2", len(g.Points))
	}
}

// TestUnknownCountryCountsTotalNotPoints: an unresolved country still moves the
// honest throughput rate but never becomes a plotted point.
func TestUnknownCountryCountsTotalNotPoints(t *testing.T) {
	a := NewTrafficAggregator()
	m := testMinute
	a.recordAt("XX", "", SvcChat, minTime(m-1))
	a.recordAt("", "", SvcChat, minTime(m-1))
	g := a.Globe(60, minTime(m))
	if len(g.Points) != 0 {
		t.Fatalf("points = %d, want 0 for unresolved countries", len(g.Points))
	}
	if g.Totals.RPS1m != round2(2.0/60.0) {
		t.Fatalf("rps_1m = %v, want %v (unknown geo still counts)", g.Totals.RPS1m, round2(2.0/60.0))
	}
}

var ipLike = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

// TestNoIPInvariant is the privacy contract: the serialized aggregate must contain
// NO IP address and NO field that could carry one. The aggregate is built by
// recording a request whose ONLY inputs are country/region/service — there is no IP
// parameter on Record to leak in the first place; this asserts the output too.
func TestNoIPInvariant(t *testing.T) {
	a := NewTrafficAggregator()
	m := testMinute
	a.recordAt("US", "CA", SvcChat, minTime(m-1))
	a.recordAt("DE", "", SvcMedia, minTime(m-1))
	b, err := json.Marshal(a.Globe(60, minTime(m)))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToLower(string(b))
	for _, bad := range []string{`"ip"`, `ipaddr`, `ip_address`, `"addr"`, `client_ip`, `remoteaddr`} {
		if strings.Contains(s, bad) {
			t.Fatalf("aggregate JSON leaked an IP-shaped field %q: %s", bad, b)
		}
	}
	if ipLike.Match(b) {
		t.Fatalf("aggregate JSON contains a dotted-quad IP: %s", b)
	}
}

// TestGlobeWireShape locks the public JSON contract keys the world proxy + globe
// layer depend on.
func TestGlobeWireShape(t *testing.T) {
	a := NewTrafficAggregator()
	m := testMinute
	a.recordAt("US", "CA", SvcChat, minTime(m-1))
	b, err := json.Marshal(a.Globe(60, minTime(m)))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{
		`"window"`, `"minutes"`, `"since"`, `"until"`,
		`"points"`, `"country"`, `"lat"`, `"lon"`, `"count"`, `"byService"`,
		`"totals"`, `"rps_1m"`, `"rpm_60m"`, `"top_countries"`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("wire shape missing %s in %s", key, s)
		}
	}
}

// TestGlobeEmptyState: a fresh aggregate returns an honest zero payload (empty
// points, zero rates) — never nil, never fabricated — so the globe's empty state is
// real.
func TestGlobeEmptyState(t *testing.T) {
	g := NewTrafficAggregator().Globe(60, minTime(testMinute))
	if g.Points == nil {
		t.Error("points must be a non-nil empty slice for a clean JSON []")
	}
	if len(g.Points) != 0 || g.Totals.RPS1m != 0 || g.Totals.RPM60m != 0 || len(g.Totals.TopCountries) != 0 {
		t.Errorf("empty aggregate not zero: %+v", g)
	}
}

// TestWindowClamp: an over-large window is capped to the ring; a non-positive one
// defaults.
func TestWindowClamp(t *testing.T) {
	a := NewTrafficAggregator()
	if g := a.Globe(999999, minTime(testMinute)); g.Window.Minutes != TrafficMaxWindowMinutes {
		t.Errorf("window not capped: %d", g.Window.Minutes)
	}
	if g := a.Globe(0, minTime(testMinute)); g.Window.Minutes != TrafficDefaultWindowMinutes {
		t.Errorf("zero window not defaulted: %d", g.Window.Minutes)
	}
}

// TestRecordTaskByTask: the router task classification folds into a region's byTask
// breakdown (in ADDITION to byService), so the globe can show what a region is doing.
func TestRecordTaskByTask(t *testing.T) {
	a := NewTrafficAggregator()
	m := testMinute
	// The edge tap counts the requests (service=chat); the router then classifies them.
	a.recordAt("US", "CA", SvcChat, minTime(m-1))
	a.recordAt("US", "CA", SvcChat, minTime(m-1))
	a.recordAt("US", "CA", SvcChat, minTime(m-1))
	a.recordTaskAt("US", "CA", "code", minTime(m-1))
	a.recordTaskAt("US", "CA", "code", minTime(m-1))
	a.recordTaskAt("US", "CA", "reasoning", minTime(m-1))

	g := a.Globe(60, minTime(m))
	if len(g.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(g.Points))
	}
	p := g.Points[0]
	if p.Count != 3 || p.ByService[SvcChat] != 3 {
		t.Errorf("count/byService wrong: %+v", p)
	}
	if p.ByTask["code"] != 2 || p.ByTask["reasoning"] != 1 {
		t.Errorf("byTask = %v, want code:2 reasoning:1", p.ByTask)
	}
	// byTask is a SUBSET of count (3 tasks over 3 requests here, but never exceeds).
	sum := 0
	for _, n := range p.ByTask {
		sum += n
	}
	if sum > p.Count {
		t.Errorf("byTask sum %d exceeds count %d", sum, p.Count)
	}
}

// TestRecordTaskLeavesTotalsUnchanged: RecordTask enriches byTask only — it must not
// move the throughput totals (rps/rpm) or a point's count, and must NOT create a
// point for a geo the tap never counted.
func TestRecordTaskLeavesTotalsUnchanged(t *testing.T) {
	a := NewTrafficAggregator()
	m := testMinute
	a.recordAt("US", "", SvcChat, minTime(m-1))
	before := a.Globe(60, minTime(m))

	// Enrich the existing US group, and try to enrich a geo (DE) the tap never counted.
	a.recordTaskAt("US", "", "code", minTime(m-1))
	a.recordTaskAt("DE", "", "vision", minTime(m-1)) // no US-style tap → must be dropped
	after := a.Globe(60, minTime(m))

	if before.Totals.RPS1m != after.Totals.RPS1m || before.Totals.RPM60m != after.Totals.RPM60m {
		t.Errorf("RecordTask moved throughput totals: before %+v after %+v", before.Totals, after.Totals)
	}
	if len(after.Points) != 1 || after.Points[0].Country != "US" {
		t.Fatalf("RecordTask created a phantom point: %+v", after.Points)
	}
	if after.Points[0].Count != 1 {
		t.Errorf("RecordTask changed count: %d", after.Points[0].Count)
	}
	if after.Points[0].ByTask["code"] != 1 {
		t.Errorf("US byTask not recorded: %v", after.Points[0].ByTask)
	}
}

// TestByTaskNoIPInvariant: byTask must not weaken the no-IP contract, and RecordTask
// takes no IP by construction.
func TestByTaskNoIPInvariant(t *testing.T) {
	a := NewTrafficAggregator()
	m := testMinute
	a.recordAt("US", "CA", SvcChat, minTime(m-1))
	a.recordTaskAt("US", "CA", "code", minTime(m-1))
	b, err := json.Marshal(a.Globe(60, minTime(m)))
	if err != nil {
		t.Fatal(err)
	}
	if ipLike.Match(b) || strings.Contains(strings.ToLower(string(b)), `"ip"`) {
		t.Fatalf("byTask payload leaked an IP: %s", b)
	}
	if !strings.Contains(string(b), `"byTask"`) {
		t.Errorf("wire shape missing byTask: %s", b)
	}
}

// TestNormTask sanitizes router task labels.
func TestNormTask(t *testing.T) {
	cases := map[string]string{
		"code": "code", "CHEAP_CHAT": "cheap_chat", " reasoning ": "reasoning",
		"": "", "has space": "", "toolongtoolongtoolong!": "", "a1": "",
	}
	for in, want := range cases {
		if got := normTask(in); got != want {
			t.Errorf("normTask(%q) = %q, want %q", in, got, want)
		}
	}
}
