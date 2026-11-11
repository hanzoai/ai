#!/usr/bin/env python3
"""Automated P1 Video Generation Reliability QA Suite (`hanzoai/ai#134`)."""

from __future__ import annotations

from typing import Any


def validate_video_job(job: dict[str, Any]) -> tuple[bool, str]:
    if not job.get("job_id"):
        return False, "missing_job_id"
    status = job.get("status")
    if status == "failed":
        return (bool(job.get("error_reason")), "failed_with_reason" if job.get("error_reason") else "silent_failure")
    if status == "completed":
        if job.get("actual_resolution") != job.get("requested_resolution"):
            return False, "resolution_mismatch"
        if job.get("actual_aspect_ratio") != job.get("requested_aspect_ratio"):
            return False, "aspect_ratio_mismatch"
        if job.get("actual_duration_sec") != job.get("requested_duration_sec"):
            return False, "duration_mismatch"
        if int(job.get("fps", 0)) < 12:
            return False, "invalid_fps"
    return True, "ok"


def run_self_tests() -> None:
    ok, reason = validate_video_job({
        "job_id": "vid_1001",
        "status": "completed",
        "requested_resolution": "1080p",
        "actual_resolution": "1080p",
        "requested_aspect_ratio": "16:9",
        "actual_aspect_ratio": "16:9",
        "requested_duration_sec": 5,
        "actual_duration_sec": 5,
        "fps": 24,
    })
    assert ok, reason

    ok_bad, _ = validate_video_job({
        "job_id": "vid_1002",
        "status": "completed",
        "requested_resolution": "1080p",
        "actual_resolution": "720p",
        "requested_aspect_ratio": "16:9",
        "actual_aspect_ratio": "16:9",
        "requested_duration_sec": 5,
        "actual_duration_sec": 5,
        "fps": 24,
    })
    assert not ok_bad
    print("[PASS] Hanzo #134 Video Generation Reliability QA Suite verified.")


if __name__ == "__main__":
    run_self_tests()
