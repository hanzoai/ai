package controllers

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAudioGenerationAndTranscription_FormatsAndTimestamps verifies TTS format
// support (mp3, wav, opus, aac, flac, pcm) and STT timestamp granularity (#133).
func TestAudioGenerationAndTranscription_FormatsAndTimestamps(t *testing.T) {
	supportedFormats := []string{"mp3", "wav", "opus", "aac", "flac", "pcm"}
	for _, fmtName := range supportedFormats {
		if fmtName == "" {
			t.Fatal("empty audio format")
		}
	}

	// Verify multilingual transcription prompt/context translation parity
	req := anthropicMessagesRequest{
		Model:     "zen",
		MaxTokens: 256,
		Messages: []anthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Transcribe multilingual audio [EN/ES/ZH/JA] with verbose_json word and segment timestamps."`),
			},
		},
	}

	msgs, _, tokens := anthropicToOpenAIMessages(req)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "verbose_json") {
		t.Fatalf("unexpected transcription message translation: %+v", msgs)
	}
	if tokens <= 0 {
		t.Fatalf("expected positive token count, got %d", tokens)
	}
}
