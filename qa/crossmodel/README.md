# Cross-Model API Compatibility QA Suite (`#138`)

Automated P0 compatibility test suite and cross-family evaluation harness for **hanzoai/ai#138 (`[QA Bounty] [P0] Cross-Model API Compatibility`)**.

## Coverage Matrix

| Requirement (`#138`) | Go Controller Suite (`controllers/cross_model_compatibility_qa_test.go`) | Standalone Harness (`qa/crossmodel/cross_model_compatibility_qa.test.mjs`) | Status |
| :--- | :--- | :--- | :--- |
| OpenAI (`/v1/chat/completions`) & Anthropic (`/v1/messages`) translation parity across model families | `TestCrossModelCompatibility_AnthropicToOpenAITranslationParity` | `1. Identical OpenAI & Anthropic schema parity across 9 model families` | PASS |
| Reasoning effort & Anthropic thinking budget normalization (`depthOf`) | `TestCrossModelCompatibility_ReasoningDepthDialectNormalization` | Verified across `none`, `low`, `medium`, `max` tiers | PASS |
| SSE streaming (`data: {...}`, `finish_reason`, `data: [DONE]`), tool calling, and structured JSON outputs | Verified in controller & SSE harness | `2. Tool calling, parallel_tool_calls, and JSON structured output compatibility` | PASS |
| Consistent error envelopes (`400`, `401`, `403`, `429`) across Gateway & Cloud transports | `TestCrossModelCompatibility_ErrorEnvelopeConsistency` | `3. Deterministic HTTP status & error envelope structure` | PASS |

## How to Run

```bash
# 1. Run hermetic Node.js QA suite
node --test qa/crossmodel/cross_model_compatibility_qa.test.mjs

# 2. Run Go controller QA test suite
go test -v ./controllers -run "TestCrossModelCompatibility_"
```
