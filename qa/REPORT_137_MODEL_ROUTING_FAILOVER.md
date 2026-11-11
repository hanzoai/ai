# QA Audit Report: Model Routing & Failover (`hanzoai/ai#137`)

## Scope Covered
1. **Catalog & Alias Routing (`/v1/models`)**: Verifies every model ID and canonical alias resolves to the exact intended backend without undisclosed model substitution.
2. **Zero-Tolerance Silent Failure Guard**: Detects and rejects HTTP 200 responses with empty `content`, HTML gateway pages masquerading as `200 OK`, or truncated SSE streams missing `finish_reason` / `data: [DONE]`.
3. **Failover Parameter Preservation**: Ensures `tools`, `response_format`, `system` prompts, and `stream` flags are deep-copied and preserved across timeout, HTTP 429, and HTTP 5xx failover hops.
