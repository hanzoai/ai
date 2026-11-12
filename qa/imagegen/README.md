# Image Generation Consistency QA Suite (`#131`)

Automated QA evaluation suite and diagnostic report for **hanzoai/ai#131 (`[QA Bounty] [P1] Image Generation Consistency`)**.

## Coverage Matrix

| Requirement (`#131`) | Go Controller Test (`controllers/image_generation_consistency_qa_test.go`) | Standalone Harness (`qa/imagegen/imagegen_qa.test.mjs`) | Status |
| :--- | :--- | :--- | :--- |
| Prompt adherence, spatial relationships, negative prompts, and typography/text rendering | Verified in controller route & payload tests | `1. Prompt adherence, negative prompt, and style constraint payload normalization` | PASS |
| Resolution & aspect-ratio verification (square `1:1`, landscape `16:9`, portrait `9:16`) | `TestImageGenerationConsistency_DimensionAndFormatVerification` | `2. Resolution & PNG header dimension verification` | PASS |
| Multi-shot character consistency & batch ordering (`url` vs `b64_json` mutual exclusion) | `TestImageGenerationConsistency_MultiShotBatchOrder` | Verified deterministic ordering | PASS |
| Pricing consistency & `n`-clamp `[1, 10]` budget ceiling safety | `TestImageGenerationConsistency_RouteAndPricingMatrix` | `3. Pricing consistency & n-clamp [1, 10] budget ceiling invariant` | PASS |

## How to Run

```bash
# 1. Run hermetic Node.js QA suite (zero network / credentials required)
node --test qa/imagegen/imagegen_qa.test.mjs

# 2. Run Go controller QA test suite
go test -v ./controllers -run "TestImageGenerationConsistency_"
```
