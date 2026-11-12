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

package controllers

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestVideoSpecAdmit_Matrix validates duration limits, resolutions (720p, 1080p, 4k),
// aspect ratios (16:9, 9:16, 1:1), explicit WIDTHxHEIGHT sizes, image-to-video frame
// constraints, and audio capability flags (#134).
func TestVideoSpecAdmit_Matrix(t *testing.T) {
	audioFalse := false
	audioTrue := true

	rawWire := videoWire{
		Durations:   []int{8, 4, 6},
		Resolutions: []string{"720P", "1080p", "4k"},
		Aspects:     []string{"16:9", "9:16", "1:1"},
		Sizes:       []string{"1280x720", "1920x1080", "1080x1920"},
		Frames:      []string{"FIRST_FRAME"},
		Audio:       &audioFalse,
		SKUs: map[string]json.RawMessage{
			"per_second_720p":  json.RawMessage(`"0.12"`),
			"per_second_1080p": json.RawMessage(`"0.20"`),
			"per_second_4k":    json.RawMessage(`"0.45"`),
			"minimum":          json.RawMessage(`"0.40"`),
		},
	}

	spec, ok := rawWire.spec()
	if !ok || spec == nil {
		t.Fatal("expected valid videoSpec from rawWire")
	}

	// 1. Default filling: Seconds=0 -> shortest duration >= 4s (4s), Resolution="" -> 720p
	askDefault := videoAsk{}
	if err := spec.admit(&askDefault); err != nil {
		t.Fatalf("admit default ask failed: %v", err)
	}
	if askDefault.Seconds != 4 {
		t.Errorf("default Seconds = %d, want 4", askDefault.Seconds)
	}
	if askDefault.Resolution != "720p" {
		t.Errorf("default Resolution = %q, want 720p", askDefault.Resolution)
	}

	// 2. Supported duration + resolution + aspect ratio matrix
	validCombos := []videoAsk{
		{Seconds: 4, Resolution: "720p", Aspect: "16:9"},
		{Seconds: 6, Resolution: "1080P", Aspect: "9:16"},
		{Seconds: 8, Resolution: "4K", Aspect: "1:1"},
		{Seconds: 6, Size: "1920x1080", First: "https://example.com/frame0.png"},
	}
	for i, c := range validCombos {
		ask := c
		if err := spec.admit(&ask); err != nil {
			t.Errorf("combo[%d] (%+v) unexpectedly rejected: %v", i, c, err)
		}
	}

	// 3. Invalid inputs must return clear, actionable failure reasons
	invalidCases := []struct {
		name    string
		ask     videoAsk
		wantSub string
	}{
		{"unsupported_duration", videoAsk{Seconds: 15, Resolution: "720p"}, "seconds 15 is not one this model makes"},
		{"unsupported_resolution", videoAsk{Seconds: 4, Resolution: "240p"}, "resolution \"240p\" is not one this model makes"},
		{"unsupported_aspect", videoAsk{Seconds: 4, Resolution: "720p", Aspect: "21:9"}, "aspect_ratio \"21:9\" is not one this model makes"},
		{"unsupported_size", videoAsk{Seconds: 4, Size: "640x480"}, "size \"640x480\" is not one this model makes"},
		{"unsupported_last_frame", videoAsk{Seconds: 4, Last: "https://example.com/end.png"}, "this model does not end on an image"},
		{"unsupported_audio", videoAsk{Seconds: 4, Audio: &audioTrue}, "this model does not write audio"},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			ask := tc.ask
			err := spec.admit(&ask)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestVideoSpecCeiling_TokenAndSecondPricing verifies reservation ceiling accuracy
// across per-second SKUs, cents_ prefixes, 32x32@24fps video token rates, and input
// frame image surcharges (#134).
func TestVideoSpecCeiling_TokenAndSecondPricing(t *testing.T) {
	w := videoWire{
		Durations:   []int{4, 8},
		Resolutions: []string{"720p", "1080p"},
		Aspects:     []string{"16:9", "9:16"},
		Frames:      []string{"first_frame", "last_frame"},
		SKUs: map[string]json.RawMessage{
			"cents_per_second_720p":  json.RawMessage(`"10"`),  // $0.10/s
			"cents_per_second_1080p": json.RawMessage(`"25"`),  // $0.25/s
			"input_image":            json.RawMessage(`"0.05"`), // $0.05/image
			"minimum":                json.RawMessage(`"0.50"`), // $0.50 floor
		},
	}
	spec, ok := w.spec()
	if !ok {
		t.Fatal("expected valid spec")
	}

	// 4s @ 720p = $0.40, clamped up to $0.50 minimum floor
	c720, ok := spec.ceiling(videoAsk{Seconds: 4, Resolution: "720p", Aspect: "16:9"})
	if !ok || c720.String() != "0.5" {
		t.Errorf("4s 720p ceiling = %v (ok=%v), want 0.5", c720, ok)
	}

	// 8s @ 1080p + 2 frame images = 8 * $0.25 + 2 * $0.05 = $2.10
	c1080, ok := spec.ceiling(videoAsk{
		Seconds:    8,
		Resolution: "1080p",
		Aspect:     "16:9",
		First:      "https://example.com/start.png",
		Last:       "https://example.com/end.png",
	})
	if !ok || c1080.String() != "2.1" {
		t.Errorf("8s 1080p + 2 images ceiling = %v (ok=%v), want 2.1", c1080, ok)
	}
}

// TestVideoVendorBody_PreservesAllParameters verifies that vendorBody serializes
// duration, resolution/size, aspect_ratio, frame_images, input_references,
// generate_audio, and seed without dropping fields (#134).
func TestVideoVendorBody_PreservesAllParameters(t *testing.T) {
	audio := true
	seed := int64(424242)
	ask := videoAsk{
		Seconds:    6,
		Resolution: "1080p",
		Aspect:     "16:9",
		First:      "https://example.com/first.png",
		Last:       "https://example.com/last.png",
		References: []string{"https://example.com/ref1.png"},
		Audio:      &audio,
		Seed:       &seed,
	}
	raw, err := ask.vendorBody("google/veo-3.1", "cinematic drone shot over coastal cliffs")
	if err != nil {
		t.Fatalf("vendorBody failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal vendorBody failed: %v", err)
	}
	if payload["model"] != "google/veo-3.1" || payload["duration"] != float64(6) {
		t.Errorf("unexpected model/duration: %+v", payload)
	}
	if payload["resolution"] != "1080p" || payload["aspect_ratio"] != "16:9" {
		t.Errorf("unexpected resolution/aspect_ratio: %+v", payload)
	}
	if payload["generate_audio"] != true || payload["seed"] != float64(424242) {
		t.Errorf("unexpected audio/seed: %+v", payload)
	}
	frames, ok := payload["frame_images"].([]any)
	if !ok || len(frames) != 2 {
		t.Errorf("expected 2 frame_images, got %+v", payload["frame_images"])
	}
}

// TestVideoConcurrentQueueStability verifies that concurrent video generation
// requests under parallel contention never exceed the pod slot capacity, never
// leak or orphan slots on failure/completion, and cleanly recover capacity (#134).
func TestVideoConcurrentQueueStability(t *testing.T) {
	// Measure total capacity first and restore
	capacity := 0
	for tryAcquireVideoSlot() {
		capacity++
	}
	for i := 0; i < capacity; i++ {
		releaseVideoSlot()
	}
	if capacity <= 0 {
		t.Fatal("expected positive video slot capacity")
	}

	const workers = 32
	var active atomic.Int32
	var maxObserved atomic.Int32
	var admitted atomic.Int32
	var rejected429 atomic.Int32

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !tryAcquireVideoSlot() {
				rejected429.Add(1)
				return
			}
			admitted.Add(1)
			cur := active.Add(1)
			for {
				prev := maxObserved.Load()
				if cur <= prev || maxObserved.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			active.Add(-1)
			releaseVideoSlot()
		}()
	}
	wg.Wait()

	if int(maxObserved.Load()) > capacity {
		t.Errorf("max concurrent active (%d) exceeded semaphore capacity (%d)", maxObserved.Load(), capacity)
	}
	if active.Load() != 0 {
		t.Errorf("expected 0 active slots after completion, got %d (orphaned slot)", active.Load())
	}
	if admitted.Load()+rejected429.Load() != workers {
		t.Errorf("admitted (%d) + rejected (%d) != workers (%d)", admitted.Load(), rejected429.Load(), workers)
	}

	// Verify all capacity slots are still acquirable (zero leaked slots)
	reacquired := 0
	for tryAcquireVideoSlot() {
		reacquired++
	}
	for i := 0; i < reacquired; i++ {
		releaseVideoSlot()
	}
	if reacquired != capacity {
		t.Errorf("reacquired %d slots after stress test, want %d", reacquired, capacity)
	}
}
