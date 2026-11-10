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
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/stt"
	openai "github.com/hanzoai/go-openai"
)

// The cross-modal chains of issue #130 fail or pass on one thing: whether the
// artifact one endpoint emits still parses as input at the next. These tests pin
// those handoffs at the wire shapes this gateway ACTUALLY emits and reads — its
// own request/response types, not a mock server's idea of OpenAI — so they run
// deterministically in CI with no key and no network, and any future drift in a
// stage's payload breaks a named test instead of breaking a customer's pipeline.

// TestCrossModalHandoffImageArtifactFeedsVisionQA walks chain 1 (Text → Image →
// Vision QA) across the two boundaries that can drop the picture: projecting the
// generated image into the OpenAI images body, and embedding that artifact back
// into a chat request as an image_url part. It also pins the measured finding
// that response_format is accepted but never honored on the non-zen path: the
// entry carries whatever the upstream produced, so a caller that asked for
// "b64_json" against a URL-returning model (every fal diffusion model) receives
// a url and NO b64_json — the handoff must branch on what arrived.
func TestCrossModalHandoffImageArtifactFeedsVisionQA(t *testing.T) {
	const picture = "https://fal.media/files/flux/img-1.jpg"

	// Stage 1 → stage 2: what ImagesGenerations emits for a fal (URL-returning) result.
	body, err := json.Marshal(map[string]any{
		"created": 1763000000,
		"data":    imageResponseData(&model.ImageGenResult{Images: []model.GeneratedImage{{URL: picture}}}),
	})
	if err != nil {
		t.Fatalf("marshal images body: %v", err)
	}
	var emitted struct {
		Created int64 `json:"created"`
		Data    []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &emitted); err != nil {
		t.Fatalf("images body does not parse as the OpenAI images shape: %v (body=%s)", err, body)
	}
	if len(emitted.Data) != 1 || emitted.Data[0].URL != picture {
		t.Fatalf("images artifact lost across projection: %s", body)
	}
	if emitted.Data[0].B64JSON != "" {
		t.Errorf("a URL-returning upstream produced b64_json=%q; the entry must carry only what exists", emitted.Data[0].B64JSON)
	}

	// The measured mismatch, pinned: response_format parses fine and changes nothing.
	var req imagesGenerationsRequest
	if err := json.Unmarshal([]byte(`{"model":"zen3-image","prompt":"a cat","response_format":"b64_json"}`), &req); err != nil {
		t.Fatalf("images request with response_format failed to parse: %v", err)
	}
	if req.ResponseFormat != "b64_json" {
		t.Errorf("response_format = %q, want it parsed (and then ignored)", req.ResponseFormat)
	}

	// Stage 2 → stage 3: the URL feeds a vision turn and must survive the SDK round trip.
	vision := openai.ChatCompletionRequest{
		Model: "zen3-vl",
		Messages: []openai.ChatCompletionMessage{{
			Role: openai.ChatMessageRoleUser,
			MultiContent: []openai.ChatMessagePart{
				{Type: openai.ChatMessagePartTypeText, Text: "what is in this picture?"},
				{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: emitted.Data[0].URL, Detail: openai.ImageURLDetailAuto}},
			},
		}},
	}
	wire, err := json.Marshal(vision)
	if err != nil {
		t.Fatalf("marshal vision request: %v", err)
	}
	var reread openai.ChatCompletionRequest
	if err := json.Unmarshal(wire, &reread); err != nil {
		t.Fatalf("the gateway could not re-read its own chained vision request: %v (wire=%s)", err, wire)
	}
	if got := reread.Messages[0].MultiContent; len(got) != 2 || got[1].ImageURL == nil || got[1].ImageURL.URL != picture {
		t.Fatalf("image artifact silently dropped across the vision handoff: %s", wire)
	}

	// The base64 spelling of the same handoff (a b64-returning upstream): the
	// bytes ride as a data: URI and must arrive byte-for-byte.
	b64Only := imageResponseData(&model.ImageGenResult{Images: []model.GeneratedImage{{B64JSON: "QUJD"}}})
	if entry := b64Only[0]; entry["b64_json"] != "QUJD" || entry["url"] != "" {
		t.Errorf("b64 artifact projection = %v, want b64_json only", entry)
	}
	dataURL := "data:image/png;base64," + b64Only[0]["b64_json"]
	if got := (&openai.ChatCompletionMessage{MultiContent: []openai.ChatMessagePart{{
		Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: dataURL},
	}}}); len(got.MultiContent[0].ImageURL.URL) != len(dataURL) {
		t.Error("data-URI artifact truncated on the way into the chat part")
	}
}

// TestCrossModalHandoffTranscriptToLLMToSpeech walks chain 2 (Audio →
// Transcription → LLM → TTS). The transcript body is stage one's whole output:
// the plain format must be exactly {"text":…} — every extra key would break the
// strict decoders downstream clients run. The assistant's answer then rides
// byte-for-byte into the speech request's "input".
func TestCrossModalHandoffTranscriptToLLMToSpeech(t *testing.T) {
	const heard = "turn left at the lighthouse"

	// Stage 1 → stage 2: transcription body into the LLM.
	body := transcriptionBody("json", &stt.Transcript{Text: heard}, 3.25)
	if string(body) != `{"text":"turn left at the lighthouse"}` {
		t.Fatalf("plain transcript = %s, want exactly {\"text\":…} — the omitempty discipline is the handoff contract", body)
	}
	var asr openai.AudioResponse
	if err := json.Unmarshal(body, &asr); err != nil || asr.Text != heard {
		t.Fatalf("transcript artifact unreadable through the OpenAI audio type: %v (%s)", err, body)
	}

	chat := openai.ChatCompletionRequest{
		Model:    "zen3-nano",
		Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: asr.Text}},
	}
	chatWire, err := json.Marshal(chat)
	if err != nil {
		t.Fatalf("marshal chat request around transcript: %v", err)
	}
	var reread openai.ChatCompletionRequest
	if err := json.Unmarshal(chatWire, &reread); err != nil || reread.Messages[0].Content != heard {
		t.Fatalf("transcript silently dropped into the LLM stage: %v (%s)", err, chatWire)
	}

	// Stage 2 → stage 3: the answer into TTS input, unchanged.
	const answer = "Turn left at the lighthouse, then continue two hundred meters."
	var reply openai.ChatCompletionResponse
	if err := json.Unmarshal([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"`+answer+`"}}]}`), &reply); err != nil {
		t.Fatalf("chat response does not parse through the OpenAI type: %v", err)
	}
	if reply.Choices[0].Message.Content != answer {
		t.Fatalf("answer lost across response parsing: %q", reply.Choices[0].Message.Content)
	}

	speech := audioSpeechRequest{Model: "zen-voice-mini", Input: reply.Choices[0].Message.Content, Voice: "alloy"}
	speechWire, err := json.Marshal(speech)
	if err != nil {
		t.Fatalf("marshal speech request: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(speechWire, &fields); err != nil {
		t.Fatalf("speech wire does not parse: %v (%s)", err, speechWire)
	}
	if fields["model"] != "zen-voice-mini" || fields["input"] != answer || fields["voice"] != "alloy" {
		t.Fatalf("answer mutated across the TTS handoff: %s", speechWire)
	}
	if _, has := fields["speed"]; has {
		t.Errorf("zero speed leaked onto the wire: %s", speechWire)
	}

	// verbose_json carries its extra keys; the plain format must not.
	verbose := transcriptionBody("verbose_json", &stt.Transcript{Text: heard}, 3.25)
	var v transcriptionResponse
	if err := json.Unmarshal(verbose, &v); err != nil {
		t.Fatalf("verbose transcript does not parse: %v (%s)", err, verbose)
	}
	if v.Task != "transcribe" || v.Duration != 3.25 || v.Text != heard {
		t.Errorf("verbose transcript lost its timings metadata: %s", verbose)
	}
}

// TestCrossModalHandoffImageIntoVideoCreate pins chain 3's (Text → Image →
// Video) measured reality at this gateway: /v1/videos/generations is PROMPT-ONLY.
// An image artifact field on the create body parses (json.Unmarshal is lenient —
// no DisallowUnknownFields) and is then silently dropped, so a naive i2v handoff
// degrades to text-to-video with no error to catch it. The job's lifecycle shape
// is pinned too: poll and download must keep the metadata the chain needs, and a
// running job must not carry an "error" key its decoders would trip on.
func TestCrossModalHandoffImageIntoVideoCreate(t *testing.T) {
	var req videosGenerationsRequest
	if err := json.Unmarshal([]byte(`{"model":"zen3-video","prompt":"animate the cat photo",`+
		`"image_url":"https://fal.media/files/flux/img-1.jpg","n":2}`), &req); err != nil {
		t.Fatalf("video create body carrying an image artifact was REJECTED: %v", err)
	}
	if req.Model != "zen3-video" || req.Prompt != "animate the cat photo" {
		t.Fatalf("video create lost its own fields: %+v", req)
	}
	// The finding, pinned: the image_url arrived nowhere — this endpoint is t2v only.
	if raw, _ := json.Marshal(req); strings.Contains(string(raw), `"image_url"`) {
		t.Errorf("videosGenerationsRequest suddenly carries an image field — update this test AND the docs")
	}

	queued, err := json.Marshal(videoStatus{ID: "video_x", Object: "video", Model: "zen3-video", Status: "queued", CreatedAt: 1763000000})
	if err != nil {
		t.Fatalf("marshal queued job: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(queued, &fields); err != nil {
		t.Fatalf("queued job shape does not parse: %v (%s)", err, queued)
	}
	for _, key := range []string{"id", "object", "model", "status", "created_at", "progress"} {
		if _, has := fields[key]; !has {
			t.Errorf("job metadata key %q missing from the poll shape: %s", key, queued)
		}
	}
	if _, has := fields["error"]; has {
		t.Errorf("a running job carries an error key: %s", queued)
	}

	failed, _ := json.Marshal(videoStatus{ID: "video_x", Object: "video", Model: "zen3-video", Status: "failed", Error: &videoFailure{Message: "upstream refused"}})
	if err := json.Unmarshal(failed, &fields); err != nil {
		t.Fatalf("failed job shape does not parse: %v (%s)", err, failed)
	}
	if e, ok := fields["error"].(map[string]any); !ok || e["message"] != "upstream refused" {
		t.Errorf("failure reason lost across the poll handoff: %s", failed)
	}
}

// TestCrossModalMultiTurnChainsKeepEveryArtifact proves the issue's fourth
// requirement at the schema plane: one conversation carrying text, an image
// artifact, a transcript and a synthesized-answer summary — string content and
// content-part arrays interleaved — survives a full marshal/unmarshal round trip
// through the exact SDK types the gateway parses inbound bodies with. A silent
// context drop here is invisible in production until a model "forgets" the
// picture three turns later.
func TestCrossModalMultiTurnChainsKeepEveryArtifact(t *testing.T) {
	const picture = "data:image/png;base64,QUJDcmVhbGNhdA=="
	turns := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "draw me a cat"},
		{Role: openai.ChatMessageRoleAssistant, Content: picture}, // the images artifact handed forward
		{Role: openai.ChatMessageRoleUser, MultiContent: []openai.ChatMessagePart{
			{Type: openai.ChatMessagePartTypeText, Text: "what did my audio say? (transcript: turn left at the lighthouse)"},
			{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: picture, Detail: openai.ImageURLDetailAuto}},
		}},
		{Role: openai.ChatMessageRoleAssistant, Content: "the audio asked you to turn left at the lighthouse"},
		{Role: openai.ChatMessageRoleUser, MultiContent: []openai.ChatMessagePart{
			{Type: openai.ChatMessagePartTypeText, Text: "and the video of the cat — did it finish? status: completed progress 100"},
		}},
	}
	wire, err := json.Marshal(openai.ChatCompletionRequest{Model: "zen3-omni", Messages: turns})
	if err != nil {
		t.Fatalf("marshal multi-turn conversation: %v", err)
	}
	var reread openai.ChatCompletionRequest
	if err := json.Unmarshal(wire, &reread); err != nil {
		t.Fatalf("gateway could not re-read its own chained conversation: %v (wire=%s)", err, wire)
	}
	if len(reread.Messages) != len(turns) {
		t.Fatalf("conversation length mutated: got %d turns, want %d (%s)", len(reread.Messages), len(turns), wire)
	}
	for i, m := range reread.Messages {
		if m.Role != turns[i].Role {
			t.Errorf("turn %d role mutated to %q", i, m.Role)
		}
	}
	img := reread.Messages[2].MultiContent
	if len(img) != 2 || img[1].ImageURL == nil || img[1].ImageURL.URL != picture {
		t.Errorf("image artifact silently dropped mid-conversation: %s", wire)
	}
	if reread.Messages[0].Content != "draw me a cat" || reread.Messages[3].Content == "" {
		t.Errorf("text turns mutated across the round trip: %s", wire)
	}
}
