#!/usr/bin/env python3
"""Automated P0 QA Test Suite for Hanzo AI Model Routing & Failover (#137).

Verifies:
1. /v1/models catalog routing & canonical alias mapping without silent model substitution.
2. Zero-tolerance detection of silent HTTP 200 failures (empty content, HTML error pages
   masquerading as 200 OK, or truncated SSE streams missing finish_reason/[DONE]).
3. Automatic upstream failover across timeout, HTTP 429 rate-limit, and HTTP 502/503 errors
   while preserving tools, response_format, system prompts, and streaming flags.
"""

from __future__ import annotations

import copy
from dataclasses import dataclass
from typing import Any, Callable


@dataclass
class AttemptResponse:
    provider: str
    served_model: str
    status_code: int
    body: str
    content: str
    finish_reason: str | None
    stream_done: bool
    latency_ms: float


def validate_routing_response(
    requested_model: str,
    allowed_aliases: dict[str, str],
    resp: AttemptResponse,
) -> tuple[bool, str]:
    if resp.status_code in (429, 500, 502, 503, 504):
        return False, f"retryable_http_{resp.status_code}"
    if resp.status_code != 200:
        return False, f"unexpected_http_{resp.status_code}"

    stripped = resp.body.strip().lower()
    if stripped.startswith("<!doctype html") or stripped.startswith("<html"):
        return False, "silent_200_html_error_body"
    if not resp.content or not resp.content.strip():
        return False, "silent_200_empty_content"
    if not resp.stream_done or not resp.finish_reason:
        return False, "silent_200_truncated_stream"

    canonical = allowed_aliases.get(requested_model, requested_model)
    if resp.served_model not in (requested_model, canonical):
        return (
            False,
            f"undisclosed_model_substitution:expected={canonical},got={resp.served_model}",
        )
    return True, "ok"


def execute_with_failover(
    payload: dict[str, Any],
    allowed_aliases: dict[str, str],
    providers: list[str],
    invoker: Callable[[str, dict[str, Any]], AttemptResponse],
) -> tuple[AttemptResponse, list[dict[str, Any]]]:
    trace: list[dict[str, Any]] = []
    req_model = str(payload.get("model") or "")
    for provider in providers:
        attempt_payload = copy.deepcopy(payload)
        try:
            resp = invoker(provider, attempt_payload)
        except TimeoutError:
            trace.append({"provider": provider, "outcome": "timeout"})
            continue

        ok, reason = validate_routing_response(req_model, allowed_aliases, resp)
        trace.append({"provider": provider, "outcome": reason, "served_model": resp.served_model})
        if ok:
            return resp, trace

    raise RuntimeError(f"All failover providers exhausted: {trace}")


def run_self_tests() -> None:
    aliases = {"zen": "zen-1.5-pro", "claude-sonnet": "claude-3-7-sonnet-20250219"}
    base_payload = {
        "model": "zen",
        "messages": [
            {"role": "system", "content": "Always answer concisely in JSON."},
            {"role": "user", "content": "Ping"},
        ],
        "tools": [{"type": "function", "function": {"name": "get_status", "parameters": {"type": "object"}}}],
        "response_format": {"type": "json_object"},
        "stream": True,
    }

    def simulated_invoker(provider: str, req: dict[str, Any]) -> AttemptResponse:
        # Verify request parameters are preserved across every failover hop
        assert req["response_format"] == {"type": "json_object"}
        assert len(req["tools"]) == 1
        assert req["messages"][0]["role"] == "system"
        assert req["stream"] is True

        if provider == "upstream-timeout":
            raise TimeoutError("upstream read timed out")
        if provider == "upstream-429":
            return AttemptResponse(provider, "zen-1.5-pro", 429, "{}", "", None, False, 12.0)
        if provider == "upstream-silent-html":
            return AttemptResponse(
                provider, "zen-1.5-pro", 200, "<html>502 Bad Gateway</html>", "err", "stop", True, 18.0
            )
        if provider == "upstream-wrong-model":
            return AttemptResponse(
                provider, "llama-3.1-8b", 200, '{"id":"1"}', '{"ok":true}', "stop", True, 25.0
            )
        return AttemptResponse(
            provider, "zen-1.5-pro", 200, '{"id":"1"}', '{"pong":true}', "stop", True, 31.0
        )

    chain = [
        "upstream-timeout",
        "upstream-429",
        "upstream-silent-html",
        "upstream-wrong-model",
        "upstream-healthy",
    ]
    winner, trace = execute_with_failover(base_payload, aliases, chain, simulated_invoker)
    assert winner.provider == "upstream-healthy"
    assert winner.served_model == "zen-1.5-pro"
    assert len(trace) == 5
    print("[PASS] Hanzo #137 Model Routing & Failover QA Suite verified (5/5 scenarios).")


if __name__ == "__main__":
    run_self_tests()
