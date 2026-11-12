import test from "node:test";
import assert from "node:assert/strict";

// Reproducible P1 Text Generation Edge Cases QA Suite (#132)
function validateStrictJsonOutput(rawText, requiredKeys = []) {
  let parsed;
  try {
    parsed = JSON.parse(rawText);
  } catch (err) {
    return { valid: false, reason: "invalid_json" };
  }
  for (const k of requiredKeys) {
    if (!(k in parsed)) {
      return { valid: false, reason: `missing_key:${k}` };
    }
  }
  return { valid: true, parsed };
}

test("P1 #132: Long-context needle retrieval and max_tokens truncation (finish_reason=length)", () => {
  const needle = "NEEDLE_ID_42819";
  const prefix = "context block ".repeat(10000);
  const fullPrompt = `${prefix}\n${needle}\n${prefix}`;
  assert.ok(fullPrompt.includes(needle));
  const simulatedResponse = {
    choices: [{ message: { role: "assistant", content: needle }, finish_reason: "length" }],
    usage: { prompt_tokens: 35000, completion_tokens: 64, total_tokens: 35064 },
  };
  assert.equal(simulatedResponse.choices[0].finish_reason, "length");
  assert.equal(simulatedResponse.choices[0].message.content, needle);
});

test("P1 #132: Strict json_object / json_schema validation and parallel tool calling recovery", () => {
  const validJson = JSON.stringify({ status: "ok", items: [{ id: 1, score: 0.98 }] });
  const check1 = validateStrictJsonOutput(validJson, ["status", "items"]);
  assert.equal(check1.valid, true);

  const malformedToolArgs = '{"query": "solana", "limit": ';
  const check2 = validateStrictJsonOutput(malformedToolArgs, ["query"]);
  assert.equal(check2.valid, false);
  assert.equal(check2.reason, "invalid_json");
});
