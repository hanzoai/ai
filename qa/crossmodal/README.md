# P1 QA Bounty Report: Cross-Modal Workflows (#130)

## Audited Multi-Step Cross-Modal Chains
1. **Chain 1 (Text -> Image -> Vision QA):**
   - Step A: `/v1/chat/completions` (`streamCaptureUsage`) refines user prompt and captures token usage.
   - Step B: `/v1/images/generations` (`gpt-image-1` / `zen3-image`) generates image artifact and bills per-unit cent/nano ledger.
   - Step C: Output base64/URL image artifact is passed into multimodal `messages[].content` (`type: image_url`) for Vision QA verification.
2. **Chain 2 (Audio -> Transcription -> LLM -> TTS):**
   - Step A: `/v1/audio/transcriptions` validates upload size via `fits(body)` (`MaxTranscribeUpload = 25 MiB`) and `MaxDecoded = 64 MiB` zstd bomb ceiling.
   - Step B: Transcribed text is processed by `/v1/chat/completions` (including multi-turn tool calls).
   - Step C: Assistant reply is validated against `MaxSpeechInput = 4096` UTF-8 bytes and synthesized via `/v1/audio/speech` (`audioSpeechRequest`).
3. **Chain 3 (Text -> Image -> Video Generation):**
   - Step A: Prompt + generated reference image (`first_frame` / `last_frame`) are handed to `google/veo-3.1`, `bytedance/seedance-2.0`, or `x-ai/grok-imagine-video-1.5-lite` (`Inputs: ["text", "image"]`).
   - Step B: `POST /v1/videos/generations` creates an async `videoJob`, polls `GET /v1/videos/{id}`, and streams `GET /v1/videos/{id}/content` under `videoSem` concurrency control.
