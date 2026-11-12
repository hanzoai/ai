# Billing & Token Accuracy QA Suite (`#136`)

Automated P0 QA test suite and audit harness for **hanzoai/ai#136 (`[QA Bounty] [P0] Billing & Token Accuracy`)**.

## Coverage Matrix

| Requirement (`#136`) | Go Controller Suite (`controllers/billing_token_accuracy_qa_test.go`) | Standalone Harness (`qa/billing/billing_token_accuracy_qa.test.mjs`) | Status |
| :--- | :--- | :--- | :--- |
| Non-streaming & streaming (`stream_options: { include_usage: true }`) token count parity | `TestBillingTokenAccuracy_TokenLinearityAndParity` | `1. Non-streaming vs streaming (include_usage: true) token & total_tokens accuracy` | PASS |
| Failed requests (`400`, `401`, `429`, `5xx`, aborted streams) are NEVER charged | `TestBillingTokenAccuracy_PreVendorFailuresNeverCharges` | `2. Zero-charge invariant on 4xx, 429, 5xx, and pre-generation stream aborts` | PASS |
| Ledger (`/v1/usage`) & per-model rate consistency (`enso-*`, `gpt-4o`, cached & reasoning tokens) | `TestBillingTokenAccuracy_FeeMarkupAndLedgerMargin` | `3. Per-model tier rates, cached prompt tokens, and reasoning token billing consistency` | PASS |

## How to Run

```bash
# 1. Run hermetic Node.js QA suite
node --test qa/billing/billing_token_accuracy_qa.test.mjs

# 2. Run Go controller QA test suite
go test -v ./controllers -run "TestBillingTokenAccuracy_"
```
