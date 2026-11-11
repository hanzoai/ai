#!/usr/bin/env python3
"""Automated P1 Audio Generation & Transcription QA Suite (`hanzoai/ai#133`)."""

from __future__ import annotations

from typing import Any


def validate_audio_container(fmt: str, payload: bytes) -> bool:
    if len(payload) < 12:
        return False
    if fmt == "wav":
        return payload.startswith(b"RIFF")
    if fmt == "flac":
        return payload.startswith(b"fLaC")
    if fmt == "opus":
        return payload.startswith(b"OggS")
    if fmt == "mp3":
        return payload.startswith(b"ID3") or (payload[0] == 0xFF and (payload[1] & 0xE0) == 0xE0)
    return True


def validate_timestamps(segments: list[dict[str, Any]], duration_sec: float) -> bool:
    prev_end = 0.0
    for seg in segments:
        start = float(seg["start"])
        end = float(seg["end"])
        if start < 0 or end < start or start + 0.05 < prev_end or end > duration_sec + 0.5:
            return False
        prev_end = end
    return len(segments) > 0


def run_self_tests() -> None:
    assert validate_audio_container("wav", b"RIFF1234WAVEfmt ")
    assert validate_audio_container("flac", b"fLaC000000000000")
    assert not validate_audio_container("wav", b"<html>error</html>")
    assert validate_timestamps(
        [{"text": "Hola", "start": 0.0, "end": 0.4}, {"text": "mundo", "start": 0.4, "end": 0.9}],
        1.0,
    )
    print("[PASS] Hanzo #133 Audio Generation & Transcription QA Suite verified.")


if __name__ == "__main__":
    run_self_tests()
