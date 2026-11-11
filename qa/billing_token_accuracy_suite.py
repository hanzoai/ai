#!/usr/bin/env python3
"""Automated P0 Billing & Token Accuracy Audit Suite (`hanzoai/ai#136`)."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass
class BillingEvent:
    model: str
    status_code: int
    prompt_tokens: int
    cached_tokens: int
    completion_tokens: int
    reasoning_tokens: int
    total_tokens: int
    input_rate_per_1m: float
    cached_rate_per_1m: float
    output_rate_per_1m: float
    charged_usd: float


def verify_billing_event(ev: BillingEvent) -> tuple[bool, str]:
    if ev.status_code >= 400:
        if ev.charged_usd > 0.0:
            return False, f"failed_http_{ev.status_code}_charged_{ev.charged_usd}"
        return True, "zero_charge_on_failure_verified"

    if ev.cached_tokens > ev.prompt_tokens:
        return False, "cached_exceeds_prompt"

    expected_total = ev.prompt_tokens + ev.completion_tokens + ev.reasoning_tokens
    if ev.total_tokens not in (expected_total, ev.prompt_tokens + ev.completion_tokens):
        return False, f"total_token_mismatch:{ev.total_tokens}!={expected_total}"

    uncached = ev.prompt_tokens - ev.cached_tokens
    expected_usd = (
        uncached * ev.input_rate_per_1m
        + ev.cached_tokens * ev.cached_rate_per_1m
        + (ev.completion_tokens + ev.reasoning_tokens) * ev.output_rate_per_1m
    ) / 1_000_000.0

    if abs(ev.charged_usd - expected_usd) > 1e-7:
        return False, f"cost_drift:charged={ev.charged_usd},expected={expected_usd}"
    return True, "ok"


def run_self_tests() -> None:
    ok_ev = BillingEvent(
        model="claude-3-7-sonnet",
        status_code=200,
        prompt_tokens=2000,
        cached_tokens=500,
        completion_tokens=800,
        reasoning_tokens=200,
        total_tokens=3000,
        input_rate_per_1m=3.0,
        cached_rate_per_1m=0.3,
        output_rate_per_1m=15.0,
        charged_usd=(1500 * 3.0 + 500 * 0.3 + 1000 * 15.0) / 1_000_000.0,
    )
    ok, msg = verify_billing_event(ok_ev)
    assert ok, msg

    err_ev = BillingEvent("zen-1.5", 503, 500, 0, 0, 0, 500, 2.0, 0.5, 8.0, 0.001)
    ok_err, _ = verify_billing_event(err_ev)
    assert not ok_err

    zero_err = BillingEvent("zen-1.5", 429, 0, 0, 0, 0, 0, 2.0, 0.5, 8.0, 0.0)
    ok_z, _ = verify_billing_event(zero_err)
    assert ok_z
    print("[PASS] Hanzo #136 Billing & Token Accuracy QA Suite verified.")


if __name__ == "__main__":
    run_self_tests()
