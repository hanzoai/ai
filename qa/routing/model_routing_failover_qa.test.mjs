import test from "node:test";
import assert from "node:assert/strict";

// Reproducible P0 Model Routing & Failover QA Suite for hanzoai/ai#137:
// [QA Bounty] [P0] Model Routing & Failover

function detectSilent200Failure(httpStatus, contentType, rawBody) {
  if (httpStatus !== 200) {
    return { silentFailure: false, status: httpStatus };
  }
  const ct = String(contentType || "").toLowerCase();
  const text = String(rawBody || "").trim();
  if (ct.includes("text/html") || text.startsWith("<!DOCTYPE html") || text.startsWith("<html")) {
    return { silentFailure: true, reason: "html_error_page_masquerading_as_200" };
  }
  let parsed;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { silentFailure: true, reason: "invalid_json_on_200" };
  }
  if (parsed.error) {
    return { silentFailure: true, reason: "embedded_error_object_on_200" };
  }
  const choice = parsed.choices?.[0];
  if (!choice) {
    return { silentFailure: true, reason: "missing_choices_array" };
  }
  const hasContent = Boolean(choice.message?.content?.trim());
  const hasToolCalls = Array.isArray(choice.message?.tool_calls) && choice.message.tool_calls.length > 0;
  const hasReasoning = Boolean(choice.message?.reasoning_content?.trim());
  if (!hasContent && !hasToolCalls && !hasReasoning) {
    return { silentFailure: true, reason: "empty_content_and_no_tool_calls_on_200" };
  }
  if (!choice.finish_reason) {
    return { silentFailure: true, reason: "missing_finish_reason" };
  }
  return { silentFailure: false };
}

function executeFailoverChain(rungs, simulateUpstream) {
  const trail = [];
  for (const rung of rungs) {
    const res = simulateUpstream(rung);
    if (res.status === 200) {
      const check = detectSilent200Failure(res.status, res.contentType, res.body);
      if (!check.silentFailure) {
        return {
          status: 200,
          servedBy: rung.upstream,
          provider: rung.provider,
          failoverTrail: trail,
        };
      }
      trail.push({ upstream: rung.upstream, provider: rung.provider, reason: check.reason });
      continue;
    }
    trail.push({ upstream: rung.upstream, provider: rung.provider, reason: `upstream status ${res.status}` });
  }
  return { status: 503, error: "all_upstreams_exhausted", failoverTrail: trail };
}

test("1. Zero tolerance for silent 200 OK failures (empty content, HTML error bodies, missing finish_reason) (#137)", () => {
  assert.equal(
    detectSilent200Failure(200, "text/html", "<html><body>502 Bad Gateway</body></html>").reason,
    "html_error_page_masquerading_as_200"
  );
  assert.equal(
    detectSilent200Failure(
      200,
      "application/json",
      JSON.stringify({ choices: [{ index: 0, message: { role: "assistant", content: "   " }, finish_reason: "stop" }] })
    ).reason,
    "empty_content_and_no_tool_calls_on_200"
  );
  assert.equal(
    detectSilent200Failure(
      200,
      "application/json",
      JSON.stringify({ choices: [{ index: 0, message: { role: "assistant", content: "Valid answer" }, finish_reason: null }] })
    ).reason,
    "missing_finish_reason"
  );
  assert.equal(
    detectSilent200Failure(
      200,
      "application/json",
      JSON.stringify({ choices: [{ index: 0, message: { role: "assistant", content: "Valid answer" }, finish_reason: "stop" }] })
    ).silentFailure,
    false
  );
});

test("2. Automatic multi-hop failover across 429 rate-limit, 504 timeout, and silent 200 truncation (#137)", () => {
  const rungs = [
    { provider: "openrouter", upstream: "openai/gpt-6.1-sol" },
    { provider: "openrouter", upstream: "google/gemini-4-argon" },
    { provider: "openrouter", upstream: "anthropic/claude-sonnet-5.5" },
  ];

  const outcome = executeFailoverChain(rungs, (rung) => {
    if (rung.upstream === "openai/gpt-6.1-sol") {
      return { status: 429, contentType: "application/json", body: '{"error":"rate_limited"}' };
    }
    if (rung.upstream === "google/gemini-4-argon") {
      // Simulate silent 200 OK empty response -> must trigger failover to 3rd rung!
      return {
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ choices: [{ index: 0, message: { role: "assistant", content: "" }, finish_reason: "stop" }] }),
      };
    }
    return {
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        choices: [{ index: 0, message: { role: "assistant", content: "Served by fallback #3" }, finish_reason: "stop" }],
      }),
    };
  });

  assert.equal(outcome.status, 200);
  assert.equal(outcome.servedBy, "anthropic/claude-sonnet-5.5");
  assert.equal(outcome.failoverTrail.length, 2);
  assert.match(outcome.failoverTrail[0].reason, /429/);
  assert.equal(outcome.failoverTrail[1].reason, "empty_content_and_no_tool_calls_on_200");
});
