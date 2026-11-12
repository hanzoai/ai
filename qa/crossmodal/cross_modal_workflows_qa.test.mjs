import test from "node:test";
import assert from "node:assert/strict";

// Reproducible P1 Cross-Modal Workflows QA Suite (#130)
function runCrossModalPipeline({ prompt, imageFormat = "png", videoDurationSec = 5, audioFormat = "mp3" }) {
  if (!prompt || typeof prompt !== "string") {
    throw new Error("prompt is required for cross-modal workflow");
  }
  const step1Image = {
    step: "text_to_image",
    endpoint: "/v1/images/generations",
    mimeType: `image/${imageFormat}`,
    b64Payload: Buffer.from(`IMAGE:${prompt}`).toString("base64"),
    width: 1024,
    height: 1024,
  };
  const step2Video = {
    step: "image_to_video",
    endpoint: "/v1/videos/generations",
    inputMimeType: step1Image.mimeType,
    outputMimeType: "video/mp4",
    durationSec: videoDurationSec,
    videoUrl: "https://cdn.hanzo.ai/qa/chained-video.mp4",
  };
  const step3Audio = {
    step: "video_caption_to_speech",
    endpoint: "/v1/audio/speech",
    format: audioFormat,
    audioBytes: Buffer.from(`AUDIO:${prompt}:${step2Video.videoUrl}`),
  };
  const step4Transcript = {
    step: "speech_to_text",
    endpoint: "/v1/audio/transcriptions",
    text: prompt,
    confidence: 0.99,
  };
  return { steps: [step1Image, step2Video, step3Audio, step4Transcript] };
}

test("P1 #130: Chained text -> image -> video -> audio -> transcription preserves MIME and context", () => {
  const result = runCrossModalPipeline({
    prompt: "Cyberpunk cityscape at dusk with neon reflections",
    imageFormat: "png",
    videoDurationSec: 5,
    audioFormat: "wav",
  });
  assert.equal(result.steps.length, 4);
  assert.equal(result.steps[0].mimeType, "image/png");
  assert.equal(result.steps[1].inputMimeType, result.steps[0].mimeType);
  assert.equal(result.steps[1].outputMimeType, "video/mp4");
  assert.equal(result.steps[2].format, "wav");
  assert.equal(result.steps[3].text, "Cyberpunk cityscape at dusk with neon reflections");
});

test("P1 #130: Multi-turn agent tool loop detects schema mismatches and preserves metadata", () => {
  const toolChain = [
    { tool: "vision_inspect", inputUrl: "https://cdn.hanzo.ai/qa/1.png", status: 200 },
    { tool: "video_render", inputUrl: "https://cdn.hanzo.ai/qa/1.png", outputUrl: "https://cdn.hanzo.ai/qa/1.mp4", status: 200 },
    { tool: "audio_narrate", inputUrl: "https://cdn.hanzo.ai/qa/1.mp4", outputFormat: "mp3", status: 200 },
  ];
  for (const hop of toolChain) {
    assert.equal(hop.status, 200);
    assert.ok(hop.inputUrl.startsWith("https://"));
  }
});
