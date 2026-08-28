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
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// TestResolveModelRoute_Video proves the raw do-ai text-to-video id resolves to
// itself on do-ai, unbranded, and premium (the zen video family is served by the
// zen service, not routed here).
func TestResolveModelRoute_Video(t *testing.T) {
	// Unbranded passthrough: the raw catalog id resolves to itself, no brand.
	route := resolveModelRoute("wan2-2-t2v-a14b")
	if route == nil || route.providerName != "do-ai" || route.upstreamModel != "wan2-2-t2v-a14b" {
		t.Fatalf("wan2-2-t2v-a14b passthrough resolve failed: %+v", route)
	}
	if route.ownedBy != "" {
		t.Errorf("unbranded passthrough ownedBy = %q, want empty", route.ownedBy)
	}
	// premium: true even though unbranded — ALL video is premium, so every video
	// unit is tagged premium in the usage record and billed per video via
	// videoCostCents. (Access itself is gated on a positive prepaid balance.)
	if !route.premium {
		t.Errorf("wan2-2-t2v-a14b (raw id) must be premium (all video is premium)")
	}
}

// TestAllVideoModelsArePremium pins the invariant that EVERY billable video id —
// branded or the raw passthrough — is flagged premium, so every video unit is
// tagged premium in the usage record. Iterating the pricing table (the
// authoritative set of video models) means a newly-added video id that forgets
// the flag fails here.
func TestAllVideoModelsArePremium(t *testing.T) {
	if len(videoPricePerVideoCents) == 0 {
		t.Fatal("videoPricePerVideoCents is empty — no video models to check")
	}
	for m := range videoPricePerVideoCents {
		route := resolveModelRoute(m)
		if route == nil {
			t.Errorf("video model %q has no route", m)
			continue
		}
		if !route.premium {
			t.Errorf("video model %q must be premium (all video is premium)", m)
		}
	}
}

// TestResolveModelRoute_VideoCaseInsensitive: the raw do-ai video id resolves
// case-insensitively like every other route.
func TestResolveModelRoute_VideoCaseInsensitive(t *testing.T) {
	route := resolveModelRoute("WAN2-2-T2V-A14B")
	if route == nil || route.upstreamModel != "wan2-2-t2v-a14b" {
		t.Fatalf("case-insensitive resolve failed: %+v", route)
	}
}

// TestVideoCostCents bills per video by user-facing name AND upstream id, with a
// conservative floor for an unknown video model (never $0), and is higher than
// the image rate.
func TestVideoCostCents(t *testing.T) {
	cases := []struct {
		model string
		n     int
		want  int64
	}{
		{"zen3-video", 1, 40},
		{"zen3-video", 2, 80},
		{"zen3-video-fast", 1, 40},
		{"zen3-video-pro", 3, 120},
		{"wan2-2-t2v-a14b", 1, 40},
		{"WAN2-2-T2V-A14B", 1, 40},          // case-insensitive
		{"some-unknown-video-model", 1, 40}, // floor
		{"zen3-video", 0, 0},                // n<=0 is free
		{"zen3-video", -1, 0},
	}
	for _, tc := range cases {
		if got := videoCostCents(tc.model, tc.n); got != tc.want {
			t.Errorf("videoCostCents(%q, %d) = %d, want %d", tc.model, tc.n, got, tc.want)
		}
	}

	// A video costs strictly more than an image (task invariant: per-video cost
	// is higher than image).
	if videoCostCents("zen3-video", 1) <= imageCostCents("zen3-image", 1) {
		t.Errorf("video per-unit (%d) must exceed image per-unit (%d)",
			videoCostCents("zen3-video", 1), imageCostCents("zen3-image", 1))
	}
}

// TestVideoNClampCeiling documents the money-safety invariant on the per-video
// cost table: the reservation the async create handler holds is
// videoCostCents(model, 1), and the settle on completion is the same, so the
// debit is bounded by the table and never an unbounded/negative amount. This
// checks the table stays positive across a small ceiling.
func TestVideoNClampCeiling(t *testing.T) {
	const maxN = 4
	for model, per := range videoPricePerVideoCents {
		if got := videoCostCents(model, maxN); got != per*maxN {
			t.Errorf("videoCostCents(%q, %d) = %d, want %d (ceiling)", model, maxN, got, per*maxN)
		}
		if videoCostCents(model, maxN) <= 0 {
			t.Errorf("ceiling cost for %q must be positive", model)
		}
	}
}

// TestVideoJobResponse projects a job into the OpenAI Sora-shaped video object
// the client sees at create/poll — id, object=="video", model, status, progress
// — and NEVER leaks the upstream id/provider/key. A failed job surfaces its
// scrubbed reason under error.message; queued/completed carry no error.
func TestVideoJobResponse(t *testing.T) {
	// queued
	q := &videoJob{id: "video_abc", userModel: "zen3-video", createdAt: time.Now(), status: "queued"}
	rq := videoJobResponse(q)
	if rq.ID != "video_abc" || rq.Object != "video" || rq.Model != "zen3-video" {
		t.Fatalf("queued projection wrong: %+v", rq)
	}
	if rq.Status != "queued" || rq.Progress != 0 {
		t.Errorf("queued status/progress wrong: %+v", rq)
	}
	if rq.Error != nil {
		t.Errorf("queued job must not carry error: %+v", rq)
	}
	if rq.CreatedAt == 0 {
		t.Errorf("projection must carry created_at")
	}
	// The upstream id must never appear anywhere in the projection — asked of the
	// JSON the client actually receives, so a nested field is covered too.
	q.upstreamID = "upstream_secret_id"
	sent, err := json.Marshal(videoJobResponse(q))
	if err != nil {
		t.Fatalf("projection does not marshal: %v", err)
	}
	if strings.Contains(string(sent), "upstream_secret_id") {
		t.Errorf("projection leaked upstream id: %s", sent)
	}

	// completed → progress 100, no error
	c := &videoJob{id: "video_c", userModel: "zen3-video", createdAt: time.Now(), hold: &budgetHold{}}
	if !c.markCompleted(40) {
		t.Fatal("first markCompleted must report first=true")
	}
	if c.markCompleted(40) {
		t.Fatal("second markCompleted must be idempotent (first=false)")
	}
	rc := videoJobResponse(c)
	if rc.Status != "completed" || rc.Progress != 100 {
		t.Errorf("completed projection wrong: %+v", rc)
	}

	// failed → error.message carries the scrubbed reason
	f := &videoJob{id: "video_f", userModel: "zen3-video", createdAt: time.Now(), hold: &budgetHold{}}
	if !f.markFailed("failed") {
		t.Fatal("first markFailed must report first=true")
	}
	if f.markFailed("failed") {
		t.Fatal("second markFailed must be idempotent (first=false)")
	}
	f.setFailureReason("upstream said nope")
	rf := videoJobResponse(f)
	if rf.Status != "failed" {
		t.Errorf("failed status wrong: %+v", rf)
	}
	if rf.Error == nil || rf.Error.Message != "upstream said nope" {
		t.Errorf("failed error projection wrong: %+v", rf)
	}
}

// TestNormalizeVideoStatus folds upstream synonyms into the OpenAI Sora
// vocabulary the client sees, and treats only completed/failed as terminal.
func TestNormalizeVideoStatus(t *testing.T) {
	cases := map[string]string{
		"queued": "queued", "PENDING": "queued", "created": "queued", "": "queued",
		"in_progress": "in_progress", "processing": "in_progress", "running": "in_progress",
		"completed": "completed", "succeeded": "completed", "SUCCESS": "completed",
		"failed": "failed", "error": "failed", "canceled": "failed", "cancelled": "failed",
	}
	for in, want := range cases {
		if got := normalizeVideoStatus(in); got != want {
			t.Errorf("normalizeVideoStatus(%q) = %q, want %q", in, got, want)
		}
	}
	if !isTerminalVideoStatus("completed") || !isTerminalVideoStatus("failed") {
		t.Error("completed/failed must be terminal")
	}
	if isTerminalVideoStatus("queued") || isTerminalVideoStatus("in_progress") {
		t.Error("queued/in_progress must not be terminal")
	}
}

// TestVideoUpstreamBase derives the /videos base from the provider URL via the
// single endpoint map, with no trailing slash so the client's
// "/videos" join is clean.
func TestVideoUpstreamBase(t *testing.T) {
	cases := []struct {
		name string
		prov object.Provider
		want string
	}{
		{"do-ai", object.Provider{Type: "DigitalOcean", ProviderUrl: "https://inference.do-ai.run/v1"}, "https://inference.do-ai.run/v1"},
		{"do-ai-no-v1", object.Provider{Type: "DigitalOcean", ProviderUrl: "https://inference.do-ai.run"}, "https://inference.do-ai.run/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.prov
			if got := upstreamBase(&p); got != tc.want {
				t.Errorf("videoUpstreamBase = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestVideoSemaphore_429WhenFull proves the pod-wide concurrency ceiling: once
// every generation slot is taken, the next acquire fails (the handler answers
// 429 rather than starting another minutes-long, memory-heavy generation that
// could OOM this single-replica pod). Releasing a slot admits exactly one more.
// The pool is fully restored so no held slot leaks into other tests.
func TestVideoSemaphore_429WhenFull(t *testing.T) {
	// Drain to capacity, tracking how many we took so we can restore exactly
	// (the size is set by VIDEO_MAX_CONCURRENT at init; the test is size-agnostic).
	taken := 0
	for tryAcquireVideoSlot() {
		taken++
	}
	if taken == 0 {
		t.Fatal("expected at least one video slot to be available")
	}
	// At capacity: the next acquire must fail — this is the request that gets 429.
	if tryAcquireVideoSlot() {
		releaseVideoSlot()
		t.Fatal("tryAcquireVideoSlot must fail when the semaphore is full")
	}
	// Freeing exactly one slot admits exactly one more, then we are full again.
	releaseVideoSlot()
	if !tryAcquireVideoSlot() {
		t.Fatal("a released slot must be re-acquirable")
	}
	if tryAcquireVideoSlot() {
		releaseVideoSlot()
		t.Fatal("only one slot should have been freed")
	}
	// Restore the pool to empty (held == taken right now) so other tests — and a
	// real handler — see a clean semaphore.
	for i := 0; i < taken; i++ {
		releaseVideoSlot()
	}
}
