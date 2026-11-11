#!/usr/bin/env python3
"""Automated P1 Text Generation Edge Cases QA Suite (`hanzoai/ai#132`)."""

from __future__ import annotations

import json


def validate_text_edge_case(
    *,
    json_mode: bool,
    max_tokens: int,
    completion_tokens: int,
    finish_reason: str,
    content: str,
    tool_call_ids: list[str],
) -> tuple[bool, str]:
    if json_mode:
        try:
            parsed = json.loads(content)
            if not isinstance(parsed, dict):
                return False, "json_root_not_object"
        except Exception as exc:
            return False, f"invalid_json:{exc}"

    if max_tokens > 0 and completion_tokens >= max_tokens and finish_reason != "length":
        return False, f"missing_length_finish_reason:{finish_reason}"

    if len(set(tool_call_ids)) != len(tool_call_ids) or any(not x for x in tool_call_ids):
        return False, "invalid_or_duplicate_parallel_tool_call_ids"

    return True, "ok"


def run_self_tests() -> None:
    ok, msg = validate_text_edge_case(
        json_mode=True,
        max_tokens=128,
        completion_tokens=64,
        finish_reason="stop",
        content='{"needle":"haystack_passkey_9481"}',
        tool_call_ids=["call_a", "call_b"],
    )
    assert ok, msg

    ok_trunc, _ = validate_text_edge_case(
        json_mode=False,
        max_tokens=32,
        completion_tokens=32,
        finish_reason="stop",
        content="truncated text",
        tool_call_ids=[],
    )
    assert not ok_trunc
    print("[PASS] Hanzo #132 Text Generation Edge Cases QA Suite verified.")


if __name__ == "__main__":
    run_self_tests()
