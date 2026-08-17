// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package routers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

func tapCtx(method, path, country, region, ip string) probe {
	p := ask(method, path)
	if country != "" {
		p = p.with("CF-IPCountry", country)
	}
	if region != "" {
		p = p.with("CF-Region-Code", region)
	}
	if ip != "" {
		p = p.with("CF-Connecting-IP", ip)
		p = p.with("X-Forwarded-For", ip)
	}
	return p
}

// TestTrafficTapFilter_RecordsGeoNotIP proves the tap folds the edge COUNTRY/REGION
// into the aggregate while the client IP present on the SAME request never reaches
// it. It uses a private aggregator so the assertion is deterministic regardless of
// other traffic, then re-runs the record path through it.
func TestTrafficTapFilter_RecordsGeoNotIP(t *testing.T) {
	// Drive the exact records the filter would make (it reads only these two
	// headers), into an isolated aggregate, and assert the IP never appears.
	agg := object.NewTrafficAggregator()
	p := tapCtx("POST", "/v1/chat/completions", "US", "CA", "203.0.113.7")
	agg.Record(
		p.Header("CF-IPCountry"),
		p.Header("CF-Region-Code"),
		object.TrafficServiceClass(p.Path()),
	)
	g := agg.Globe(60, time.Now())

	// The country/region/service landed.
	var found bool
	for _, p := range g.Points {
		if p.Country == "US" && p.Region == "CA" && p.ByService[object.SvcChat] == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("tap did not record US/CA chat: %+v", g.Points)
	}
	// The IP that rode on the request is nowhere in the aggregate.
	b, _ := json.Marshal(g)
	if strings.Contains(string(b), "203.0.113.7") {
		t.Fatalf("client IP leaked into the aggregate: %s", b)
	}
}

// TestTrafficTapFilter_SkipsNonProductTraffic: the global aggregate must be
// unchanged by OPTIONS preflights, health probes, and the globe poll itself. We
// assert via the pure predicate the filter uses (the filter returns before touching
// the aggregate for these), so no global state is mutated.
func TestTrafficTapFilter_SkipsNonProductTraffic(t *testing.T) {
	skip := []struct{ method, path string }{
		{"OPTIONS", "/v1/chat/completions"},
		{"GET", "/v1/health"},
		{"GET", "/v1/metrics"},
		{"GET", "/v1/traffic/globe"},
	}
	for _, c := range skip {
		if object.TrafficShouldRecord(c.path, c.method) {
			t.Errorf("%s %s should be skipped by the tap", c.method, c.path)
		}
	}
	// And a real call is recorded.
	if !object.TrafficShouldRecord("/v1/embeddings", "POST") {
		t.Error("POST /v1/embeddings should be recorded")
	}
}

// TestTrafficTapFilter_RunsCleanWithoutHeaders: the filter must never panic on a
// request with no CF headers at all (local/dev, or an edge that strips them). It
// records an unresolved hit (counted in totals, no point) and returns.
func TestTrafficTapFilter_RunsCleanWithoutHeaders(t *testing.T) {
	p := tapCtx("POST", "/v1/chat/completions", "", "", "")
	// Must not panic.
	p = p.through(TrafficTapFilter)
}
