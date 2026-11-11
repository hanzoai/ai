# QA Audit Report: Text Generation Edge Cases (`hanzoai/ai#132`)

- Validates 32k/128k/256k needle-in-a-haystack retrieval, strict `json_object` / `json_schema` validity, parallel tool call ID uniqueness, and `finish_reason: "length"` truncation invariants.
