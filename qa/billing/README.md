# P0 QA Bounty Report: Billing & Token Accuracy (#136)

## Scope & Architecture Audited
1. **Exact Integer Nano-USD Ledger (`controllers/billing_nano.go` & `controllers/billing_cents_test.go`):**
   - Audited `nanoPerToken`, `tokenNanoAt`, `cacheReadRate`, `cacheSavedNano`, `fresh`, `usageCostNano`, and `nanoToCents`.
   - Confirmed anti-cents-flooring invariant: sub-cent completions (e.g., 200 tokens at $6.60/1M = 1,320,000 nano-USD = $0.00132) are tracked in exact integer nano-USD ($10^{-9}$) without float rounding or $0.01 minimum overcharging.
2. **Streaming Token Capture & Fallback Tokenizer (`controllers/billing_reserve.go`):**
   - Audited `streamCaptureUsage` and `sniffZenUsage` across `stream_options: { include_usage: true }` SSE chunks.
   - Verified 8 MiB scanner line buffer prevents silent dropping of long tool-call deltas and trailing `usage` chunks.
   - Verified both `delta.content` and `delta.tool_calls[].function.arguments` are accumulated into `completionText` for reference tokenizer fallback when an upstream omits usage.
3. **Failed Request Zero-Charge Guarantee (`controllers/videos_api.go`, `controllers/billing_reserve.go`, `controllers/zap_billing-usage.go`):**
   - Verified 400 validation errors, 401/403 authentication rejections, 429 rate-limit rejections, 5xx upstream failures, and aborted SSE streams before token emission incur **zero** balance deduction.
4. **Multimodal & Cache Consistency:**
   - Prompt cache reads default to 10% of input rate when unspecified (`cacheReadRate`), and `fresh(prompt, read, write)` prevents double-charging cached tokens at the full prompt rate.
   - Image (`gpt-image-1`) and video (`wan2-2-t2v-a14b`, `zen3-video`) per-unit cent rates project cleanly through `nanoToCents(usageCostNano(r)) == usageCostCents(r)`.
