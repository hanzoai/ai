# Video Generation Reliability QA Suite (`#134`)

Automated QA test suite and diagnostic report for **hanzoai/ai#134 (`[QA Bounty] [P1] Video Generation Reliability`)**.

## Coverage Matrix

| Requirement (`#134`) | Go Controller Test (`controllers/video_generation_reliability_qa_test.go`) | Standalone Harness (`qa/videogen/videogen_qa.test.mjs`) | Status |
| :--- | :--- | :--- | :--- |
| Duration limits (`4s..15s`), resolutions (`720p`, `1080p`, `4k`), aspect ratios (`16:9`, `9:16`, `1:1`) | `TestVideoSpecAdmit_Matrix` | `1. Duration, resolution, and aspect-ratio matrix admission` | PASS |
| Pricing reservation bounds (`ceiling`) & video token math (`32x32 @ 24fps`) | `TestVideoSpecCeiling_TokenAndSecondPricing` | Verified against SKU tables | PASS |
| Text-to-video & image-to-video parameter fidelity (`first_frame`, `last_frame`, `seed`, `generate_audio`) | `TestVideoVendorBody_PreservesAllParameters` | Verified in request serialization | PASS |
| Job polling lifecycle (`queued` -> `in_progress` -> `completed`/`failed`) & upstream secret scrubbing | `TestVideoJobResponse`, `TestNormalizeVideoStatus` | `2. Job polling lifecycle & upstream secret scrubbing` | PASS |
| Concurrent video generation queue stability, 429 admission control, and zero orphaned slots | `TestVideoConcurrentQueueStability` | `3. Concurrent video generation semaphore & 429 admission control` | PASS |

## How to Run

```bash
# 1. Run hermetic Node.js QA suite (zero network / credentials required)
node --test qa/videogen/videogen_qa.test.mjs

# 2. Run Go controller QA test suite
go test -v ./controllers -run "TestVideoSpecAdmit_Matrix|TestVideoSpecCeiling_TokenAndSecondPricing|TestVideoVendorBody_PreservesAllParameters|TestVideoConcurrentQueueStability"
```
