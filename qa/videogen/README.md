# P1 QA Bounty Report: Video Generation Reliability (#134)

## Scope & Architecture Audited
- Async Sora-Compatible Surface (controllers/videos_api.go & controllers/zap_videos-generation.go):
  - POST /v1/videos/generations (videos.generate)
  - GET /v1/videos/{id} (videos.retrieve)
  - GET /v1/videos/{id}/content (videos.content)
- Vendor Catalog & Spec Validation (controllers/video_spec.go & controllers/video_vendor.go):
  - Audited google/veo-3.1, bytedance/seedance-2.0, x-ai/grok-imagine-video-1.5-lite, and wan2-2-t2v-a14b.
- In-Pod Job Lifecycle & Concurrency Control (controllers/video_jobs.go):
  - Verified maxOutstandingVideoJobs = 256 flood ceiling, videoJobTTL = 15m reaper eviction, videoJobRetention = 30m, and videoSem (defaultVideoMaxConcurrent = 2) download slot limiter.

## Key Reliability Findings & Boundary Matrix
1. Duration Limits: veo-3.1 (4s, 6s, 8s), seedance-2.0 (4s..15s), grok-imagine-video-1.5-lite (1s..8s) - Verified.
2. Resolutions & Aspect Ratios: 480p, 720p, 1080p, 4k across 16:9, 9:16, 1:1 - Verified.
3. Job Lifecycle & Polling: queued -> in_progress -> completed / failed with exact-once settlement - Verified.
4. Concurrency & Download Limiter: tryAcquireVideoSlot() bounds simultaneous MP4 memory relays and returns HTTP 429 on burst contention - Verified.
