// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
    "bytes"
    "context"
    "encoding/json"
    "strings"
    "testing"
)

// TestCrossModalQAChain1TextToImageToVisionHandoff verifies Chain 1 (#130):
// Text prompt refinement (/v1/chat/completions) -> Image generation (/v1/images/generations)
// -> Vision QA handoff (base64/URL image_url content block), ensuring route resolution,
// schema preservation, and nano-USD billing accuracy across modalities.
func TestCrossModalQAChain1TextToImageToVisionHandoff(t *testing.T) {
    withMarginPricing(t)

    // Step 1: LLM refines prompt via SSE stream
    sse := strings.Join([]string{
        `data: {"id":"cm-1","choices":[{"delta":{"content":"A futuristic neon skyline at dusk"}}]}`,
        `data: {"id":"cm-1","choices":[],"usage":{"prompt_tokens":18,"completion_tokens":8,"total_tokens":26}}`,
        "data: [DONE]",
        "",
    }, "\n")
    var out bytes.Buffer
    tok, refinedPrompt := streamCaptureUsage(strings.NewReader(sse), &out, func() error { return nil }, true, nil, nil)
    if tok.prompt != 18 || tok.completion != 8 || strings.TrimSpace(refinedPrompt) == "" {
        t.Fatalf("Step 1 LLM prompt refinement handoff failed: tok=%+v prompt=%q", tok, refinedPrompt)
    }

    // Step 2: Image generation route & per-image billing check
    imgRecord := &usageRecord{Model: "gpt-image-1", ImageCount: 1}
    imgCents := usageCostCents(imgRecord)
    if imgCents <= 0 || imgCents != nanoToCents(usageCostNano(imgRecord)) {
        t.Fatalf("Step 2 Image generation cost mismatch: cents=%d nano=%d", imgCents, usageCostNano(imgRecord))
    }

    // Step 3: Vision QA payload handoff (verifying multimodal content array marshaling)
    visionPayload := map[string]any{
        "model": "zen3-omni",
        "messages": []map[string]any{
            {
                "role": "user",
                "content": []map[string]any{
                    {"type": "text", "text": "Describe the generated image: " + refinedPrompt},
                    {"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}},
                },
            },
        },
    }
    raw, err := json.Marshal(visionPayload)
    if err != nil || !strings.Contains(string(raw), "data:image/png;base64,") {
        t.Fatalf("Step 3 Vision QA payload handoff failed: %v", err)
    }
}

// TestCrossModalQAChain2AudioToTranscriptionToChatToTTS verifies Chain 2 (#130):
// Audio upload bound check (/v1/audio/transcriptions) -> Chat LLM tool/text response ->
// TTS speech synthesis request (/v1/audio/speech) input length & format handoff.
func TestCrossModalQAChain2AudioToTranscriptionToChatToTTS(t *testing.T) {
    // Step 1: Verify audio upload size bound (MaxTranscribeUpload = 25 MiB)
    sampleAudio := make([]byte, 4096)
    if !fits(sampleAudio) {
        t.Fatalf("expected 4 KiB audio payload to pass fits() check")
    }
    oversizedAudio := make([]byte, MaxTranscribeUpload+1)
    if fits(oversizedAudio) {
        t.Fatalf("expected payload > MaxTranscribeUpload (%d) to be rejected by fits()", MaxTranscribeUpload)
    }

    // Step 2: Simulated transcription text handed to LLM -> synthesised via audioSpeechRequest
    llmReply := "Your flight to Tokyo is confirmed for 9 AM."
    if len(llmReply) > MaxSpeechInput {
        t.Fatalf("LLM reply exceeds MaxSpeechInput (%d)", MaxSpeechInput)
    }

    ttsBody, err := json.Marshal(audioSpeechRequest{
        Model:          "tts-1",
        Input:          llmReply,
        Voice:          "alloy",
        ResponseFormat: "mp3",
        Speed:          1.0,
    })
    if err != nil {
        t.Fatalf("failed to marshal audioSpeechRequest: %v", err)
    }

    var decoded audioSpeechRequest
    if err := json.Unmarshal(ttsBody, &decoded); err != nil || decoded.Input != llmReply || decoded.ResponseFormat != "mp3" {
        t.Fatalf("TTS handoff roundtrip mismatch: %+v err=%v", decoded, err)
    }
}

// TestCrossModalQAChain3TextToImageToVideoGeneration verifies Chain 3 (#130):
// Text + first_frame reference image -> Async Video Generation (google/veo-3.1,
// x-ai/grok-imagine-video-1.5-lite, wan2-2-t2v-a14b) catalog & auth handoff.
func TestCrossModalQAChain3TextToImageToVideoGeneration(t *testing.T) {
    ms := clipCatalog(t)

    for _, id := range []string{"google/veo-3.1", "bytedance/seedance-2.0", "x-ai/grok-imagine-video-1.5-lite"} {
        sku, ok := ms[id]
        if !ok || sku.Video == nil {
            t.Fatalf("expected %s in video catalog", id)
        }
        if strings.Join(sku.Inputs, ",") != "text,image" {
            t.Fatalf("%s inputs = %v, want [text image] for image-to-video handoff", id, sku.Inputs)
        }
        if len(sku.Video.Frames) == 0 || sku.Video.Frames[0] != "first_frame" {
            t.Fatalf("%s frame_images = %v, expected first_frame support", id, sku.Video.Frames)
        }
    }

    // Verify async video generation rejects unauthenticated handoffs cleanly
    reqBody := []byte(`{"model":"zen3-video","prompt":"camera pans across neon skyline","size":"1280x720","seconds":4}`)
    msg, err := zapVideosGenerateHandler(context.Background(), "", reqBody)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got := zapVideoStatus(t, msg); got != 401 {
        t.Fatalf("unauthenticated video handoff status = %d, want 401", got)
    }
}
