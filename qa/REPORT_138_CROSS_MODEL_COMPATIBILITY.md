# QA Audit Report: Cross-Model API Compatibility (`hanzoai/ai#138`)

## Compatibility Matrix (9 Model Families)
| Model Family | `/v1/chat/completions` & `/v1/messages` | SSE Chunk Contract (`[DONE]`) | Multi-Turn `role: "tool"` | `json_object` / `json_schema` | Error Envelope (`400/401/404/422/429/500`) |
|---|---|---|---|---|---|
| `zen` | PASS | PASS | PASS | PASS | PASS |
| `enso` | PASS | PASS | PASS | PASS | PASS |
| `kai` | PASS | PASS | PASS | PASS | PASS |
| `claude` | PASS | PASS | PASS | PASS | PASS |
| `gpt` | PASS | PASS | PASS | PASS | PASS |
| `gemini` | PASS | PASS | PASS | PASS | PASS |
| `qwen` | PASS | PASS | PASS | PASS | PASS |
| `deepseek` | PASS | PASS | PASS | PASS | PASS |
| `llama` | PASS | PASS | PASS | PASS | PASS |
