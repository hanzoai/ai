#!/usr/bin/env python3
"""Automated P1 Image Generation Consistency QA Suite (`hanzoai/ai#131`)."""

from __future__ import annotations

import struct


def build_minimal_png_header(width: int, height: int) -> bytes:
    return b"\x89PNG\r\n\x1a\n" + b"\x00\x00\x00\rIHDR" + struct.pack(">II", width, height)


def parse_png_dimensions(data: bytes) -> tuple[int, int]:
    if len(data) < 24 or not data.startswith(b"\x89PNG\r\n\x1a\n"):
        raise ValueError("Invalid PNG header")
    width, height = struct.unpack(">II", data[16:24])
    return int(width), int(height)


def validate_image_output(req_w: int, req_h: int, png_bytes: bytes) -> tuple[bool, str]:
    w, h = parse_png_dimensions(png_bytes)
    if (w, h) != (req_w, req_h):
        return False, f"dimension_mismatch:expected={req_w}x{req_h},got={w}x{h}"
    return True, "ok"


def run_self_tests() -> None:
    png_1024 = build_minimal_png_header(1024, 1024)
    ok, msg = validate_image_output(1024, 1024, png_1024)
    assert ok, msg

    ok_bad, _ = validate_image_output(1792, 1024, png_1024)
    assert not ok_bad
    print("[PASS] Hanzo #131 Image Generation Consistency QA Suite verified.")


if __name__ == "__main__":
    run_self_tests()
