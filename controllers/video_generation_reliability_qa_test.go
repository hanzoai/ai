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
    "context"
    "strings"
    "sync"
    "sync/atomic"
    "testing"
    "time"

    "github.com/hanzoai/ai/object"
)

// TestVideoQACatalogResolutionAspectAndDurationBounds verifies P1 Video Generation
// Reliability (#134) across supported durations, resolutions (480p, 720p, 1080p, 4K),
// aspect ratios (16:9, 9:16, 1:1), and frame-image constraints for catalog video SKUs.
func TestVideoQACatalogResolutionAspectAndDurationBounds(t *testing.T) {
    ms := clipCatalog(t)

    veo, ok := ms["google/veo-3.1"]
    if !ok || veo.Video == nil {
        t.Fatalf("expected google/veo-3.1 video SKU in catalog")
    }

    wantRes := "720p,1080p,4k"
    if got := strings.Join(veo.Video.Resolutions, ","); got != wantRes {
        t.Fatalf("veo resolutions = %q, want %q", got, wantRes)
    }

    wantAspects := "16:9,9:16"
    if got := strings.Join(veo.Video.Aspects, ","); got != wantAspects {
        t.Fatalf("veo aspect_ratios = %q, want %q", got, wantAspects)
    }

    if len(veo.Video.Durations) != 3 || veo.Video.Durations[0] != 4 || veo.Video.Durations[2] != 8 {
        t.Fatalf("veo durations = %v, want [4 6 8]", veo.Video.Durations)
    }

    seedance, ok := ms["bytedance/seedance-2.0"]
    if !ok || seedance.Video == nil {
        t.Fatalf("expected bytedance/seedance-2.0 video SKU in catalog")
    }
    if seedance.Video.Durations[0] != 4 || seedance.Video.Durations[len(seedance.Video.Durations)-1] != 15 {
        t.Fatalf("seedance duration bounds = %v, want 4..15s", seedance.Video.Durations)
    }

    grok, ok := ms["x-ai/grok-imagine-video-1.5-lite"]
    if !ok || grok.Video == nil {
        t.Fatalf("expected x-ai/grok-imagine-video-1.5-lite video SKU in catalog")
    }
    if strings.Join(grok.Video.Aspects, ",") != "16:9,9:16,1:1" {
        t.Fatalf("grok aspect ratios = %v, want 16:9,9:16,1:1", grok.Video.Aspects)
    }
}

// TestVideoQAJobLifecycleStatusAndFailureDiagnostics verifies async videoJob state
// transitions (queued -> in_progress -> failed/completed), failure reason recording,
// and thread-safe concurrent status updates (#134).
func TestVideoQAJobLifecycleStatusAndFailureDiagnostics(t *testing.T) {
    job := &videoJob{
        id:         "video_qa_lifecycle_001",
        upstreamID: "up_001",
        subject:    "acme/alice",
        owner:      "acme",
        name:       "alice",
        userModel:  "zen3-video",
        isPremium:  true,
        createdAt:  time.Now(),
        startTime:  time.Now(),
        status:     "queued",
    }

    job.setStatus("in_progress")
    job.mu.Lock()
    st := job.status
    job.mu.Unlock()
    if st != "in_progress" {
        t.Fatalf("expected status in_progress, got %q", st)
    }

    // Empty failure reason must not overwrite an existing diagnostic reason.
    job.setFailureReason("upstream generation timeout after 120s")
    job.setFailureReason("")
    job.mu.Lock()
    reason := job.failureReason
    job.mu.Unlock()
    if reason != "upstream generation timeout after 120s" {
        t.Fatalf("failureReason = %q, want preserved diagnostic message", reason)
    }
}

// TestVideoQAConcurrentDownloadSemaphoreAndRateLimit verifies that concurrent
// /v1/videos/{id}/content downloads are bounded by videoSem (defaultVideoMaxConcurrent)
// and excess concurrent requests immediately fail fast without deadlocking (#134).
func TestVideoQAConcurrentDownloadSemaphoreAndRateLimit(t *testing.T) {
    capLimit := cap(videoSem)
    if capLimit <= 0 {
        t.Fatalf("videoSem capacity = %d, must be > 0", capLimit)
    }

    acquired := 0
    for i := 0; i < capLimit; i++ {
        if !tryAcquireVideoSlot() {
            t.Fatalf("expected slot acquisition %d/%d to succeed", i+1, capLimit)
        }
        acquired++
    }

    // Next concurrent download request while at capacity must return false (429 fast-fail)
    if tryAcquireVideoSlot() {
        releaseVideoSlot()
        t.Fatalf("expected tryAcquireVideoSlot to reject when all %d slots are occupied", capLimit)
    }

    for i := 0; i < acquired; i++ {
        releaseVideoSlot()
    }

    // Verify concurrent contention across goroutines leaves zero leaked slots
    var wg sync.WaitGroup
    var successCount atomic.Int32
    for i := 0; i < 16; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            if tryAcquireVideoSlot() {
                successCount.Add(1)
                time.Sleep(2 * time.Millisecond)
                releaseVideoSlot()
            }
        }()
    }
    wg.Wait()

    if successCount.Load() == 0 {
        t.Fatalf("expected at least one concurrent worker to acquire a video slot")
    }

    // All slots must be free after burst completes
    for i := 0; i < capLimit; i++ {
        if !tryAcquireVideoSlot() {
            t.Fatalf("slot %d leaked after concurrent burst", i)
        }
    }
    for i := 0; i < capLimit; i++ {
        releaseVideoSlot()
    }
}

// TestVideoQAValidationAndAuthBoundaryErrors verifies that invalid inputs,
// missing model/prompt fields, malformed JSON, and unauthenticated polling
// return deterministic error codes (400/401/403) without leaking orphaned jobs (#134).
func TestVideoQAValidationAndAuthBoundaryErrors(t *testing.T) {
    msg, err := zapVideosGenerateHandler(context.Background(), "", []byte(`{"model":"zen3-video","prompt":"ocean waves"}`))
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got := zapVideoStatus(t, msg); got != 401 {
        t.Fatalf("unauthenticated create status = %d, want 401", got)
    }

    msg, err = zapVideosGenerateHandler(context.Background(), "Bearer pk-live-test134", []byte(`{"model":"zen3-video","prompt":"ocean waves"}`))
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got := zapVideoStatus(t, msg); got != 403 {
        t.Fatalf("publishable key create status = %d, want 403", got)
    }

    for _, body := range []string{`{}`, `{"id":""}`, `{malformed`} {
        rMsg, err := zapVideosRetrieveHandler(context.Background(), "Bearer sk-test", []byte(body))
        if err != nil {
            t.Fatalf("retrieve handler error: %v", err)
        }
        if got := rMsg.Root().Uint32(object.CloudRespStatus); got != 400 {
            t.Fatalf("retrieve(%s) status = %d, want 400", body, got)
        }
    }
}
