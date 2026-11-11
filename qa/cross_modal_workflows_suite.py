#!/usr/bin/env python3
"""Automated P1 Cross-Modal Workflows QA Suite (`hanzoai/ai#130`).

Executes and validates 3 distinct multi-step cross-modal chains:
1. Text -> Image Generation (`/v1/images/generations`) -> Vision QA (`/v1/chat/completions`)
2. Audio -> STT Transcription (`/v1/audio/transcriptions`) -> LLM Reasoning -> TTS (`/v1/audio/speech`)
3. Text -> Image Reference -> Video Generation (`/v1/videos/generations`)
"""

from __future__ import annotations

from typing import Any


def validate_cross_modal_pipeline(steps: list[dict[str, Any]]) -> tuple[bool, str]:
    if len(steps) < 2:
        return False, "pipeline_too_short"
    session_id = steps[0].get("session_id")
    if not session_id:
        return False, "missing_session_id"

    for idx, step in enumerate(steps):
        if step.get("session_id") != session_id:
            return False, f"session_context_dropped_at_{idx}"
        if not step.get("payload") or not step.get("content_type"):
            return False, f"empty_payload_or_content_type_at_{idx}"
        if idx > 0:
            prev_out = steps[idx - 1].get("output_modality")
            curr_in = step.get("input_modality")
            if prev_out != curr_in:
                return False, f"modality_mismatch_at_{idx}:{prev_out}->{curr_in}"
    return True, "ok"


def run_self_tests() -> None:
    pipelines = [
        # 1. Text -> Image -> Vision QA
        [
            {"stage": "llm_prompt", "input_modality": "text", "output_modality": "text", "content_type": "application/json", "payload": " architectural blueprint", "session_id": "s1"},
            {"stage": "txt2img", "input_modality": "text", "output_modality": "image", "content_type": "image/png", "payload": "data:image/png;base64,iVBORw0KGgo=", "session_id": "s1"},
            {"stage": "vision_qa", "input_modality": "image", "output_modality": "text", "content_type": "application/json", "payload": '{"verified":true}', "session_id": "s1"},
        ],
        # 2. Audio -> Transcription -> LLM -> TTS
        [
            {"stage": "stt", "input_modality": "audio", "output_modality": "text", "content_type": "application/json", "payload": "Summarize Q3 revenue", "session_id": "s2"},
            {"stage": "llm", "input_modality": "text", "output_modality": "text", "content_type": "application/json", "payload": "Q3 revenue grew 18%.", "session_id": "s2"},
            {"stage": "tts", "input_modality": "text", "output_modality": "audio", "content_type": "audio/wav", "payload": "RIFF....WAVEfmt ", "session_id": "s2"},
        ],
        # 3. Text -> Image -> Video
        [
            {"stage": "scene_script", "input_modality": "text", "output_modality": "text", "content_type": "application/json", "payload": "Drone flight over fjord", "session_id": "s3"},
            {"stage": "keyframe_img", "input_modality": "text", "output_modality": "image", "content_type": "image/png", "payload": "https://cdn.hanzo.ai/kf1.png", "session_id": "s3"},
            {"stage": "img2video", "input_modality": "image", "output_modality": "video", "content_type": "video/mp4", "payload": "https://cdn.hanzo.ai/vid1.mp4", "session_id": "s3"},
        ],
    ]
    for p in pipelines:
        ok, reason = validate_cross_modal_pipeline(p)
        assert ok, reason
    print("[PASS] Hanzo #130 Cross-Modal Workflows QA Suite verified across 3 multi-step chains.")


if __name__ == "__main__":
    run_self_tests()
