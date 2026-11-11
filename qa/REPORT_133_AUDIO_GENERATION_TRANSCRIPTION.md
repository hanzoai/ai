# QA Audit Report: Audio Generation & Transcription (`hanzoai/ai#133`)

- Validates `/v1/audio/speech` binary container headers (`mp3`, `wav`, `opus`, `flac`) and `/v1/audio/transcriptions` word/segment timestamp monotonicity across multilingual fixtures.
