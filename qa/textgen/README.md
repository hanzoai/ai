# P1 QA Bounty Report: Text Generation Edge Cases (`#132`)

## Overview
Provides automated Go controller tests and Node.js QA suites covering long-context needle retrieval (`32k`–`256k+` tokens), strict JSON schema validation (`json_object` / `json_schema`), parallel tool calling, `reasoning_content` / `thinking` depth normalization, and `finish_reason: "length"` truncation behavior.

## Reproduction
```bash
node --test qa/textgen/text_generation_edge_cases_qa.test.mjs
go test -v ./controllers -run TestTextGenerationEdgeCases
```
