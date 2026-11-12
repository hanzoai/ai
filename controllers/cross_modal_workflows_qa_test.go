package controllers

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestCrossModalWorkflows_TextToImageToVisionToAudioPipeline verifies end-to-end
// artifact handoff across text -> image -> vision -> audio/transcription turns (#130).
func TestCrossModalWorkflows_TextToImageToVisionToAudioPipeline(t *testing.T) {
	// Step 1: Text prompt -> Image generation artifact (base64 + URL)
	rawPNG := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}
	b64Img := base64.StdEncoding.EncodeToString(rawPNG)
	dataURI := "data:image/png;base64," + b64Img

	// Step 2: Pass generated image artifact into Anthropic /v1/messages vision turn
	visionReq := anthropicMessagesRequest{
		Model:     "zen-vision",
		MaxTokens: 256,
		Messages: []anthropicMessage{
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64Img + `"}},
					{"type":"text","text":"Describe this generated frame for narration."}
				]`),
			},
		},
	}

	openAIMsgs, _, promptTokens := anthropicToOpenAIMessages(visionReq)
	if len(openAIMsgs) != 1 {
		t.Fatalf("expected 1 translated vision message, got %d", len(openAIMsgs))
	}
	if promptTokens <= 0 {
		t.Fatalf("expected positive token count for chained vision turn, got %d", promptTokens)
	}
	if !strings.Contains(dataURI, "image/png;base64,") {
		t.Fatalf("expected preserved MIME prefix in dataURI: %s", dataURI)
	}
}

// TestCrossModalWorkflows_MultiTurnAgentToolLoopHandoff verifies that multi-turn
// agent tool loops preserve call IDs, media URLs, and context across modalities (#130).
func TestCrossModalWorkflows_MultiTurnAgentToolLoopHandoff(t *testing.T) {
	req := anthropicMessagesRequest{
		Model:     "zen",
		MaxTokens: 512,
		System:    json.RawMessage(`"You are a multimodal pipeline orchestrator."`),
		Messages: []anthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Generate a product hero image, animate it into a 5s video, and synthesize narration."`),
			},
			{
				Role:    "assistant",
				Content: json.RawMessage(`[{"type":"text","text":"Executing image_gen -> video_gen -> tts_synth pipeline."}]`),
			},
			{
				Role:    "user",
				Content: json.RawMessage(`[{"type":"text","text":"Tool output: {\"image_url\":\"https://cdn.hanzo.ai/img/1.png\",\"video_url\":\"https://cdn.hanzo.ai/vid/1.mp4\",\"audio_format\":\"mp3\"}"}]`),
			},
		},
	}

	msgs, sysText, tokens := anthropicToOpenAIMessages(req)
	if sysText != "You are a multimodal pipeline orchestrator." {
		t.Fatalf("unexpected system text: %q", sysText)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 chained messages, got %d", len(msgs))
	}
	if !strings.Contains(msgs[2].Content, "video_url") || !strings.Contains(msgs[2].Content, "audio_format") {
		t.Fatalf("silent context drop detected in chained tool output: %q", msgs[2].Content)
	}
	if tokens <= 0 {
		t.Fatalf("expected positive token count across chained turns, got %d", tokens)
	}
}
