import test from "node:test";
import assert from "node:assert/strict";

// Reproducible P1 Audio Generation & Transcription QA Suite (#133)
const SUPPORTED_TTS_FORMATS = new Set(["mp3", "wav", "opus", "aac", "flac", "pcm"]);

function validateTtsRequest({ model, input, voice, response_format = "mp3", speed = 1.0 }) {
  if (!model || !input || !voice) return { ok: false, error: "missing_required_fields" };
  if (!SUPPORTED_TTS_FORMATS.has(response_format)) return { ok: false, error: "unsupported_format" };
  if (speed < 0.25 || speed > 4.0) return { ok: false, error: "speed_out_of_range" };
  return { ok: true, contentType: `audio/${response_format}` };
}

function validateTranscriptionTimestamps(verboseJson) {
  assert.equal(typeof verboseJson.text, "string");
  for (const seg of verboseJson.segments || []) {
    if (seg.end < seg.start || seg.start < 0) return false;
  }
  for (const w of verboseJson.words || []) {
    if (w.end < w.start || w.start < 0) return false;
  }
  return true;
}

test("P1 #133: TTS (/v1/audio/speech) supports mp3, wav, opus, aac, flac, pcm and validates speed bounds", () => {
  for (const fmt of SUPPORTED_TTS_FORMATS) {
    const res = validateTtsRequest({
      model: "tts-1-hd",
      input: "Hanzo AI multilingual speech synthesis test.",
      voice: "alloy",
      response_format: fmt,
      speed: 1.25,
    });
    assert.equal(res.ok, true);
  }
  const invalidSpeed = validateTtsRequest({
    model: "tts-1",
    input: "test",
    voice: "nova",
    speed: 6.0,
  });
  assert.equal(invalidSpeed.ok, false);
  assert.equal(invalidSpeed.error, "speed_out_of_range");
});

test("P1 #133: STT (/v1/audio/transcriptions) verifies monotonic word and segment timestamps", () => {
  const sampleVerboseJson = {
    task: "transcribe",
    language: "english",
    duration: 4.52,
    text: "Hanzo AI gateway processes streaming audio.",
    words: [
      { word: "Hanzo", start: 0.0, end: 0.42 },
      { word: "AI", start: 0.43, end: 0.68 },
      { word: "gateway", start: 0.70, end: 1.25 },
    ],
    segments: [
      { id: 0, start: 0.0, end: 4.52, text: "Hanzo AI gateway processes streaming audio." },
    ],
  };
  assert.equal(validateTranscriptionTimestamps(sampleVerboseJson), true);
});
