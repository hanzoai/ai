# QA Audit Report: Billing & Token Accuracy (`hanzoai/ai#136`)

- Verifies `usage.prompt_tokens`, `usage.completion_tokens`, `usage.reasoning_tokens`, `usage.cached_tokens`, and `usage.total_tokens` across sync and streaming (`stream_options: { include_usage: true }`) calls.
- Enforces the strict `$0.00` charge invariant on all `4xx`, `429`, and `5xx` responses.
