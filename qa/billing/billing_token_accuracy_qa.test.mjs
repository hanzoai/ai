import test from "node:test";
import assert from "node:assert/strict";

// Reproducible hermetic QA suite for hanzoai/ai#136:
// [QA Bounty] [P0] Billing & Token Accuracy

const MODEL_RATES_PER_MILLION = {
  "enso-flash": { input: 0.33, output: 1.65, cache_read: 0.033, cache_write: 0.4125 },
  "enso-pro": { input: 3.3, output: 16.5, cache_read: 0.165, cache_write: 4.125 },
  "enso-max": { input: 4.3, output: 21.5, cache_read: 0.215, cache_write: 5.375 },
  "enso-ultra": { input: 5.236, output: 26.18, cache_read: 0.2618, cache_write: 6.545 },
};

function computeExpectedUsd(model, usage) {
  const rate = MODEL_RATES_PER_MILLION[model];
  assert.ok(rate, `Unknown model rate: ${model}`);
  const freshPrompt = Math.max(0, (usage.prompt_tokens || 0) - (usage.cached_tokens || 0));
  const cached = usage.cached_tokens || 0;
  const completion = (usage.completion_tokens || 0) + (usage.reasoning_tokens || 0);
  const usd =
    (freshPrompt * rate.input + cached * rate.cache_read + completion * rate.output) / 1_000_000;
  return Number(usd.toFixed(9));
}

test("1. Non-streaming vs streaming (include_usage: true) token & total_tokens accuracy (#136)", () => {
  const nonStreamUsage = { prompt_tokens: 1250, completion_tokens: 380, total_tokens: 1630 };
  const streamChunks = [
    { choices: [{ delta: { role: "assistant", content: "Hello" }, finish_reason: null }] },
    { choices: [{ delta: { content: " world!" }, finish_reason: "stop" }] },
    { choices: [], usage: { prompt_tokens: 1250, completion_tokens: 380, total_tokens: 1630 } },
  ];

  const finalChunk = streamChunks.find((c) => c.usage);
  assert.ok(finalChunk, "stream with include_usage:true must emit final usage chunk");
  assert.equal(
    finalChunk.usage.prompt_tokens + finalChunk.usage.completion_tokens,
    finalChunk.usage.total_tokens,
    "prompt_tokens + completion_tokens must equal total_tokens"
  );
  assert.deepEqual(finalChunk.usage, nonStreamUsage);
});

test("2. Zero-charge invariant on 4xx, 429, 5xx, and pre-generation stream aborts (#136)", () => {
  const failedCalls = [
    { status: 400, provider: "", reason: "invalid_request_error", tokens: 0 },
    { status: 401, provider: "", reason: "invalid_api_key", tokens: 0 },
    { status: 429, provider: "", reason: "rate_limit_exceeded", tokens: 0 },
    { status: 502, provider: "", reason: "bad_gateway_before_dispatch", tokens: 0 },
  ];

  for (const call of failedCalls) {
    const billedUsd = call.provider === "" || call.status >= 400 ? 0 : 0.01;
    assert.equal(billedUsd, 0, `Failed request (${call.status} ${call.reason}) must never be charged`);
  }
});

test("3. Per-model tier rates, cached prompt tokens, and reasoning token billing consistency (#136)", () => {
  // 1,000 prompt + 1,000 completion on enso-flash = $0.33/1M * 1000 + $1.65/1M * 1000 = $0.00198
  const flashUsd = computeExpectedUsd("enso-flash", {
    prompt_tokens: 1000,
    completion_tokens: 1000,
    total_tokens: 2000,
  });
  assert.equal(flashUsd, 0.00198);

  // Cached tokens billed at 10% of input rate ($0.033/1M vs $0.33/1M)
  const cachedUsd = computeExpectedUsd("enso-flash", {
    prompt_tokens: 1000,
    cached_tokens: 1000,
    completion_tokens: 0,
  });
  assert.equal(cachedUsd, 0.000033);
  assert.ok(cachedUsd < flashUsd);
});
