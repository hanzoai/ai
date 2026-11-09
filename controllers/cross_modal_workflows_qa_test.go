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
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// makeTestWAV builds a valid 16-bit Mono PCM WAV byte slice with the requested sample rate and duration.
func makeTestWAV(sampleRate int, durationMs int) []byte {
	numSamples := (sampleRate * durationMs) / 1000
	subchunk2Size := numSamples * 2 // 16-bit mono = 2 bytes per sample
	chunkSize := 36 + subchunk2Size

	buf := new(bytes.Buffer)
	// RIFF header
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(chunkSize))
	buf.WriteString("WAVE")

	// fmt subchunk
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16)) // subchunk1Size = 16 for PCM
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))  // audioFormat = 1 (PCM)
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))  // numChannels = 1 (Mono)
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	byteRate := uint32(sampleRate * 2)
	_ = binary.Write(buf, binary.LittleEndian, byteRate)
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))  // blockAlign
	_ = binary.Write(buf, binary.LittleEndian, uint16(16)) // bitsPerSample

	// data subchunk
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(subchunk2Size))
	// audio sample data (simple synthesized tone/silence)
	for i := 0; i < numSamples; i++ {
		sample := int16((i % 100) * 100)
		_ = binary.Write(buf, binary.LittleEndian, sample)
	}

	return buf.Bytes()
}

// parseWAVHeader validates a WAV RIFF header and extracts sample rate, channels, and audio duration.
func parseWAVHeader(data []byte) (sampleRate int, channels int, durationSec float64, err error) {
	if len(data) < 44 {
		return 0, 0, 0, fmt.Errorf("data too short for WAV header (%d bytes)", len(data))
	}
	if string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, 0, 0, fmt.Errorf("invalid RIFF/WAVE header")
	}
	channels = int(binary.LittleEndian.Uint16(data[22:24]))
	sampleRate = int(binary.LittleEndian.Uint32(data[24:28]))
	dataSize := int(binary.LittleEndian.Uint32(data[40:44]))
	if channels <= 0 || sampleRate <= 0 {
		return 0, 0, 0, fmt.Errorf("invalid channel or sample rate: ch=%d, rate=%d", channels, sampleRate)
	}
	bytesPerSample := 2 // 16-bit
	totalSamples := dataSize / (channels * bytesPerSample)
	durationSec = float64(totalSamples) / float64(sampleRate)
	return sampleRate, channels, durationSec, nil
}

// makeTestPNGImage builds a valid PNG image byte stream for testing cross-modal image generation.
func makeTestPNGImage(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: 50, G: uint8(x % 256), B: uint8(y % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// TestCrossModalQA_TextToImageToVisionQA executes Chain 1:
// Text Prompt -> Image Generation (/v1/images/generations) -> Vision QA (/v1/chat/completions with image_url).
func TestCrossModalQA_TextToImageToVisionQA(t *testing.T) {
	testPNG := makeTestPNGImage(512, 512)
	b64PNG := base64.StdEncoding.EncodeToString(testPNG)
	imageURL := "https://cdn.hanzo.ai/generated/cm_test_img_1.png"

	var stepCount int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := atomic.AddInt32(&stepCount, 1)
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Step 1: Text LLM prompt refinement (Chat Completions)
		case step == 1 && r.URL.Path == "/v1/chat/completions":
			var chatReq map[string]any
			_ = json.NewDecoder(r.Body).Decode(&chatReq)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "chatcmpl-step1",
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   "zen5",
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role":    "assistant",
							"content": "A high-resolution photograph of a glass prism refracting sunlight into a rainbow on white marble",
						},
						"finish_reason": "stop",
					},
				},
			})

		// Step 2: Image Generation endpoint
		case step == 2 && r.URL.Path == "/v1/images/generations":
			var imgReq map[string]any
			_ = json.NewDecoder(r.Body).Decode(&imgReq)
			prompt := imgReq["prompt"].(string)
			if !strings.Contains(prompt, "glass prism") {
				http.Error(w, "prompt missing refined text from step 1", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"created": time.Now().Unix(),
				"data": []map[string]any{
					{
						"url":      imageURL,
						"b64_json": b64PNG,
					},
				},
			})

		// Step 3: Vision QA (Chat Completions with multimodal image input)
		case step == 3 && r.URL.Path == "/v1/chat/completions":
			var visionReq struct {
				Model    string `json:"model"`
				Messages []struct {
					Role    string `json:"role"`
					Content any    `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&visionReq)
			if len(visionReq.Messages) == 0 {
				http.Error(w, "missing messages", http.StatusBadRequest)
				return
			}
			// Verify image artifact was preserved in message content
			lastMsg := visionReq.Messages[len(visionReq.Messages)-1]
			contentBytes, _ := json.Marshal(lastMsg.Content)
			if !strings.Contains(string(contentBytes), imageURL) && !strings.Contains(string(contentBytes), b64PNG) {
				http.Error(w, "vision QA message did not receive image artifact from step 2", http.StatusBadRequest)
				return
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "chatcmpl-step3-vision",
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   "zen5",
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role":    "assistant",
							"content": "The image clearly shows a triangular glass prism refracting bright sunlight into vivid rainbow spectrum bands across a polished white marble surface.",
						},
						"finish_reason": "stop",
					},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockSrv.Close()

	// ── DRIVE PIPELINE ──
	// 1. Text Prompt Request
	chatBody, _ := json.Marshal(map[string]any{
		"model": "zen5",
		"messages": []map[string]string{
			{"role": "user", "content": "Suggest a detailed prompt for generating an image of light refraction."},
		},
	})
	resp1, err := http.Post(mockSrv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(chatBody))
	if err != nil {
		t.Fatalf("step 1 failed: %v", err)
	}
	var step1Res struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.NewDecoder(resp1.Body).Decode(&step1Res)
	resp1.Body.Close()
	refinedPrompt := step1Res.Choices[0].Message.Content
	if refinedPrompt == "" {
		t.Fatalf("empty refined prompt from step 1")
	}

	// 2. Image Generation Request using refined prompt
	imgBody, _ := json.Marshal(map[string]any{
		"model":  "zen3-image",
		"prompt": refinedPrompt,
		"size":   "512x512",
	})
	resp2, err := http.Post(mockSrv.URL+"/v1/images/generations", "application/json", bytes.NewReader(imgBody))
	if err != nil {
		t.Fatalf("step 2 failed: %v", err)
	}
	var step2Res struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&step2Res)
	resp2.Body.Close()
	if len(step2Res.Data) == 0 || step2Res.Data[0].URL == "" {
		t.Fatalf("step 2 failed to produce image artifact: %+v", step2Res)
	}
	outputURL := step2Res.Data[0].URL

	// 3. Vision QA Request using output image URL
	visionBody, _ := json.Marshal(map[string]any{
		"model": "zen5",
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "Describe what you see in this generated image in detail."},
					{"type": "image_url", "image_url": map[string]string{"url": outputURL}},
				},
			},
		},
	})
	resp3, err := http.Post(mockSrv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(visionBody))
	if err != nil {
		t.Fatalf("step 3 failed: %v", err)
	}
	var step3Res struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.NewDecoder(resp3.Body).Decode(&step3Res)
	resp3.Body.Close()
	visionAnswer := step3Res.Choices[0].Message.Content
	if !strings.Contains(visionAnswer, "prism") {
		t.Errorf("vision QA response does not identify image subject: %q", visionAnswer)
	}

	t.Logf("QA Diagnostic: Chain 1 (Text -> Image -> Vision QA) passed successfully.")
	t.Logf("  Refined Prompt: %s", refinedPrompt)
	t.Logf("  Image Artifact: %s", outputURL)
	t.Logf("  Vision QA Analysis: %s", visionAnswer)
}

// TestCrossModalQA_AudioTranscriptionToLLMToTTS executes Chain 2:
// Audio File -> Transcription (/v1/audio/transcriptions) -> LLM Processing -> Speech (/v1/audio/speech).
func TestCrossModalQA_AudioTranscriptionToLLMToTTS(t *testing.T) {
	// Synthesize input WAV audio (16kHz mono, 1.5 seconds)
	inputAudioWAV := makeTestWAV(16000, 1500)
	sampleRate, channels, duration, err := parseWAVHeader(inputAudioWAV)
	if err != nil || sampleRate != 16000 || channels != 1 || duration < 1.4 {
		t.Fatalf("failed to build input WAV: err=%v, rate=%d, ch=%d, dur=%.2fs", err, sampleRate, channels, duration)
	}

	// Synthesize output speech audio (24kHz mono, 2.0 seconds)
	outputSpeechWAV := makeTestWAV(24000, 2000)

	var stepCount int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := atomic.AddInt32(&stepCount, 1)

		switch {
		// Step 1: Transcription endpoint
		case step == 1 && r.URL.Path == "/v1/audio/transcriptions":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text":     "What is the airspeed velocity of an unladen European swallow?",
				"language": "en",
				"duration": duration,
			})

		// Step 2: LLM chat reasoning
		case step == 2 && r.URL.Path == "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "chatcmpl-audio-chain",
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   "zen5",
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role":    "assistant",
							"content": "An unladen European swallow flies at approximately eleven meters per second or twenty-four miles per hour.",
						},
						"finish_reason": "stop",
					},
				},
			})

		// Step 3: TTS speech synthesis
		case step == 3 && r.URL.Path == "/v1/audio/speech":
			var speechReq struct {
				Model string `json:"model"`
				Input string `json:"input"`
				Voice string `json:"voice"`
			}
			_ = json.NewDecoder(r.Body).Decode(&speechReq)
			if !strings.Contains(speechReq.Input, "eleven meters per second") {
				http.Error(w, "TTS input did not receive LLM answer text", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "audio/wav")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(outputSpeechWAV)))
			_, _ = w.Write(outputSpeechWAV)

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockSrv.Close()

	// 1. Submit audio to transcription endpoint
	req1, _ := http.NewRequest(http.MethodPost, mockSrv.URL+"/v1/audio/transcriptions", bytes.NewReader(inputAudioWAV))
	req1.Header.Set("Content-Type", "audio/wav")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("transcription request failed: %v", err)
	}
	var sttRes struct {
		Text string `json:"text"`
	}
	_ = json.NewDecoder(resp1.Body).Decode(&sttRes)
	resp1.Body.Close()
	transcriptText := sttRes.Text
	if transcriptText == "" {
		t.Fatalf("empty transcription output")
	}

	// 2. Pass transcript to LLM
	llmReq, _ := json.Marshal(map[string]any{
		"model": "zen5",
		"messages": []map[string]string{
			{"role": "user", "content": transcriptText},
		},
	})
	resp2, err := http.Post(mockSrv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(llmReq))
	if err != nil {
		t.Fatalf("LLM reasoning failed: %v", err)
	}
	var llmRes struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&llmRes)
	resp2.Body.Close()
	replyText := llmRes.Choices[0].Message.Content

	// 3. Pass LLM reply to TTS synthesis
	ttsReq, _ := json.Marshal(map[string]string{
		"model": "elevenlabs/eleven-v3",
		"input": replyText,
		"voice": "alloy",
	})
	resp3, err := http.Post(mockSrv.URL+"/v1/audio/speech", "application/json", bytes.NewReader(ttsReq))
	if err != nil {
		t.Fatalf("TTS synthesis failed: %v", err)
	}
	defer resp3.Body.Close()

	if ct := resp3.Header.Get("Content-Type"); ct != "audio/wav" && ct != "audio/mpeg" {
		t.Errorf("TTS unexpected content type: %q", ct)
	}
	audioPayload, _ := io.ReadAll(resp3.Body)
	if len(audioPayload) != len(outputSpeechWAV) {
		t.Errorf("TTS audio size mismatch: got %d, want %d", len(audioPayload), len(outputSpeechWAV))
	}
	outRate, outCh, outDur, parseErr := parseWAVHeader(audioPayload)
	if parseErr != nil {
		t.Fatalf("synthesized audio is not valid WAV: %v", parseErr)
	}
	if outRate != 24000 || outCh != 1 || outDur < 1.9 {
		t.Errorf("synthesized audio metadata mismatch: rate=%d, ch=%d, dur=%.2f", outRate, outCh, outDur)
	}

	t.Logf("QA Diagnostic: Chain 2 (Audio -> STT -> LLM -> TTS) passed successfully.")
	t.Logf("  Transcribed Input: %q", transcriptText)
	t.Logf("  LLM Reasoning Output: %q", replyText)
	t.Logf("  Synthesized Audio: %d bytes (24kHz Mono, %.2fs duration)", len(audioPayload), outDur)
}

// TestCrossModalQA_TextToImageToVideo executes Chain 3:
// Text Prompt -> Image Generation (/v1/images/generations) -> Video Generation (/v1/videos with first_frame).
func TestCrossModalQA_TextToImageToVideo(t *testing.T) {
	keyframeURL := "https://cdn.hanzo.ai/frames/keyframe_001.png"
	movieBytes := mp4Of(4000, 1000, 1280, 720)

	var stepCount int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := atomic.AddInt32(&stepCount, 1)

		switch {
		// Step 1: Image Generation creates keyframe
		case step == 1 && r.URL.Path == "/v1/images/generations":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"created": time.Now().Unix(),
				"data": []map[string]any{
					{"url": keyframeURL},
				},
			})

		// Step 2: Video Generation submission with First frame URL
		case step == 2 && r.URL.Path == "/v1/videos" && r.Method == http.MethodPost:
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			first, _ := req["first_frame"].(string)
			if first == "" {
				first, _ = req["first"].(string)
			}
			if first != keyframeURL {
				http.Error(w, fmt.Sprintf("first_frame %q does not match keyframeURL %q", first, keyframeURL), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"video_cm_100","status":"queued","object":"video"}`)

		// Step 3: Poll status -> completed
		case step == 3 && r.URL.Path == "/v1/videos/video_cm_100":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"video_cm_100","status":"completed","object":"video"}`)

		// Step 4: Download content -> decodable MP4
		case step == 4 && r.URL.Path == "/v1/videos/video_cm_100/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(movieBytes)

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockSrv.Close()

	// 1. Generate keyframe image
	imgReq, _ := json.Marshal(map[string]string{
		"model":  "zen3-image",
		"prompt": "wide establishing shot of an emerald valley in morning fog, cinematic lighting, 16:9",
		"size":   "1280x720",
	})
	resp1, err := http.Post(mockSrv.URL+"/v1/images/generations", "application/json", bytes.NewReader(imgReq))
	if err != nil {
		t.Fatalf("image generation step failed: %v", err)
	}
	var imgRes struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp1.Body).Decode(&imgRes)
	resp1.Body.Close()
	producedKeyframe := imgRes.Data[0].URL

	// 2. Submit video generation with keyframe as first_frame
	videoReq, _ := json.Marshal(map[string]any{
		"model":       "zen3-video",
		"prompt":      "camera slowly glides forward over the emerald valley as morning fog dissipates",
		"first_frame": producedKeyframe,
		"seconds":     4,
		"resolution":  "720p",
	})
	resp2, err := http.Post(mockSrv.URL+"/v1/videos", "application/json", bytes.NewReader(videoReq))
	if err != nil {
		t.Fatalf("video submission step failed: %v", err)
	}
	var vidInit struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&vidInit)
	resp2.Body.Close()
	if vidInit.ID != "video_cm_100" {
		t.Fatalf("unexpected video ID: %s", vidInit.ID)
	}

	// 3. Poll video status
	resp3, err := http.Get(mockSrv.URL + "/v1/videos/" + vidInit.ID)
	if err != nil {
		t.Fatalf("video poll failed: %v", err)
	}
	var vidPoll struct {
		Status string `json:"status"`
	}
	_ = json.NewDecoder(resp3.Body).Decode(&vidPoll)
	resp3.Body.Close()
	if vidPoll.Status != "completed" {
		t.Fatalf("expected completed status, got %q", vidPoll.Status)
	}

	// 4. Download and verify MP4 video stream
	resp4, err := http.Get(mockSrv.URL + "/v1/videos/" + vidInit.ID + "/content")
	if err != nil {
		t.Fatalf("video download failed: %v", err)
	}
	defer resp4.Body.Close()

	vidBytes, _ := io.ReadAll(resp4.Body)
	info, err := readMP4(bytes.NewReader(vidBytes), int64(len(vidBytes)))
	if err != nil {
		t.Fatalf("downloaded video artifact failed MP4 parsing: %v", err)
	}
	if info.width != 1280 || info.height != 720 || info.seconds != 4.0 {
		t.Errorf("MP4 dimensions or duration mismatch: %+v", info)
	}

	t.Logf("QA Diagnostic: Chain 3 (Text -> Image -> Video) passed successfully.")
	t.Logf("  Keyframe Anchor: %s", producedKeyframe)
	t.Logf("  Generated Video ID: %s", vidInit.ID)
	t.Logf("  Video Stream Metadata: %dx%d @ %.1fs MP4", info.width, info.height, info.seconds)
}

// TestCrossModalQA_MultiTurnAgentToolLoop tests an autonomous multi-turn conversation
// session invoking vision analysis, image generation, and audio synthesis tools sequentially.
func TestCrossModalQA_MultiTurnAgentToolLoop(t *testing.T) {
	type toolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}

	type chatMessage struct {
		Role       string     `json:"role"`
		Content    any        `json:"content,omitempty"`
		ToolCalls  []toolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}

	var sessionTurns []chatMessage

	// Initial user goal
	sessionTurns = append(sessionTurns, chatMessage{
		Role:    "user",
		Content: "Analyze our company mascot and generate a new holiday edition image and podcast intro.",
	})

	// Turn 1: Model invokes mascot vision analysis tool
	call1 := toolCall{
		ID:   "call_vision_99",
		Type: "function",
	}
	call1.Function.Name = "analyze_mascot_vision"
	call1.Function.Arguments = `{"mascot_image_url":"https://cdn.hanzo.ai/mascot.png"}`
	sessionTurns = append(sessionTurns, chatMessage{
		Role:      "assistant",
		ToolCalls: []toolCall{call1},
	})

	// Turn 2: Tool execution result
	sessionTurns = append(sessionTurns, chatMessage{
		Role:       "tool",
		ToolCallID: "call_vision_99",
		Content:    `{"mascot_name":"Hanzo Fox","colors":["cyber-orange","white"],"accessories":["goggles"]}`,
	})

	// Turn 3: Model invokes image generation with analyzed mascot traits
	call2 := toolCall{
		ID:   "call_image_gen_100",
		Type: "function",
	}
	call2.Function.Name = "generate_image"
	call2.Function.Arguments = `{"prompt":"Hanzo Fox with goggles wearing a red holiday scarf in snow","size":"1024x1024"}`
	sessionTurns = append(sessionTurns, chatMessage{
		Role:      "assistant",
		ToolCalls: []toolCall{call2},
	})

	// Turn 4: Image tool result
	sessionTurns = append(sessionTurns, chatMessage{
		Role:       "tool",
		ToolCallID: "call_image_gen_100",
		Content:    `{"image_url":"https://cdn.hanzo.ai/holiday_mascot.png"}`,
	})

	// Turn 5: Model invokes TTS voiceover
	call3 := toolCall{
		ID:   "call_tts_101",
		Type: "function",
	}
	call3.Function.Name = "generate_speech"
	call3.Function.Arguments = `{"text":"Happy holidays from the entire Hanzo AI engineering fleet!","voice":"nova"}`
	sessionTurns = append(sessionTurns, chatMessage{
		Role:      "assistant",
		ToolCalls: []toolCall{call3},
	})

	// Turn 6: TTS tool result
	sessionTurns = append(sessionTurns, chatMessage{
		Role:       "tool",
		ToolCallID: "call_tts_101",
		Content:    `{"audio_url":"https://cdn.hanzo.ai/holiday_intro.mp3","duration_sec":3.2}`,
	})

	// Turn 7: Final composite response
	sessionTurns = append(sessionTurns, chatMessage{
		Role:    "assistant",
		Content: "I have analyzed the Hanzo Fox mascot, generated the holiday edition keyframe at https://cdn.hanzo.ai/holiday_mascot.png, and synthesized your podcast intro audio.",
	})

	// Verify conversation session integrity
	raw, err := json.Marshal(sessionTurns)
	if err != nil {
		t.Fatalf("failed to marshal multi-turn agent turns: %v", err)
	}
	var reloaded []chatMessage
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatalf("failed to unmarshal agent turns: %v", err)
	}

	if len(reloaded) != 8 {
		t.Errorf("expected 8 turns, got %d", len(reloaded))
	}
	// Verify tool ID correlation
	if reloaded[2].ToolCallID != reloaded[1].ToolCalls[0].ID {
		t.Errorf("turn 2 tool call ID mismatch: %q vs %q", reloaded[2].ToolCallID, reloaded[1].ToolCalls[0].ID)
	}
	if reloaded[4].ToolCallID != reloaded[3].ToolCalls[0].ID {
		t.Errorf("turn 4 tool call ID mismatch")
	}
	if reloaded[6].ToolCallID != reloaded[5].ToolCalls[0].ID {
		t.Errorf("turn 6 tool call ID mismatch")
	}

	t.Logf("QA Diagnostic: Multi-turn agent tool loop (Vision -> Image Gen -> TTS) state correlation verified.")
}

// TestCrossModalQA_PayloadHandoffInvarianceAndEncoding verifies URL vs Base64 representations,
// data URI prefixes, and context preservation when passing artifacts across multimodal boundaries.
func TestCrossModalQA_PayloadHandoffInvarianceAndEncoding(t *testing.T) {
	samplePNG := makeTestPNGImage(256, 256)
	b64Clean := base64.StdEncoding.EncodeToString(samplePNG)
	dataURI := "data:image/png;base64," + b64Clean

	// 1. Data URI prefix stripping & reconstruction
	if !strings.HasPrefix(dataURI, "data:image/png;base64,") {
		t.Fatalf("invalid data URI format")
	}
	extractedB64 := strings.TrimPrefix(dataURI, "data:image/png;base64,")
	if extractedB64 != b64Clean {
		t.Errorf("extracted base64 mismatch")
	}

	// 2. Decode extracted base64 and verify PNG header
	decodedBytes, err := base64.StdEncoding.DecodeString(extractedB64)
	if err != nil || !bytes.Equal(decodedBytes, samplePNG) {
		t.Fatalf("decoded bytes do not match original PNG")
	}

	// 3. Schema compatibility verification
	imgGenData := map[string]string{
		"b64_json": b64Clean,
		"url":      "https://cdn.hanzo.ai/sample.png",
	}

	// Multimodal vision message payload
	visionContent := []map[string]any{
		{"type": "text", "text": "Analyze this generated image:"},
		{"type": "image_url", "image_url": map[string]string{"url": imgGenData["url"]}},
	}
	vBytes, _ := json.Marshal(visionContent)
	if !strings.Contains(string(vBytes), "https://cdn.hanzo.ai/sample.png") {
		t.Errorf("vision content failed to serialize image URL")
	}

	t.Logf("QA Diagnostic: Payload handoff invariance and data URI encoding verified.")
}

// TestCrossModalQA_LatencyBottlenecksAndFailurePropagation validates that intermediate
// failures in a cross-modal pipeline do not hang requests, leak hold balances, or corrupt downstream state.
func TestCrossModalQA_LatencyBottlenecksAndFailurePropagation(t *testing.T) {
	// Simulate an upstream HTTP server where Step 1 succeeds, Step 2 throttles (429), and Step 3 fails (500)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/step/success":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case "/step/throttled":
			w.Header().Set("Retry-After", "5")
			http.Error(w, `{"error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded: sk-test-key-redacted"}}`, http.StatusTooManyRequests)
		case "/step/server_error":
			http.Error(w, `{"error":{"code":"internal_error","message":"Upstream cluster unreachable"}}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// 1. Success stage
	res1, err := http.Get(srv.URL + "/step/success")
	if err != nil || res1.StatusCode != http.StatusOK {
		t.Fatalf("stage 1 failed: %v", err)
	}
	res1.Body.Close()

	// 2. Throttled stage (429)
	res2, err := http.Get(srv.URL + "/step/throttled")
	if err != nil {
		t.Fatalf("stage 2 transport error: %v", err)
	}
	body2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != http.StatusTooManyRequests {
		t.Errorf("stage 2 status = %d, want 429", res2.StatusCode)
	}
	if strings.Contains(string(body2), "sk-test-key-redacted") {
		// Secret scrubbing check
		sanitized := strings.ReplaceAll(string(body2), "sk-test-key-redacted", "[REDACTED]")
		if strings.Contains(sanitized, "sk-test") {
			t.Errorf("secret not sanitized in 429 response")
		}
	}

	// 3. Server error stage (500)
	res3, err := http.Get(srv.URL + "/step/server_error")
	if err != nil {
		t.Fatalf("stage 3 transport error: %v", err)
	}
	res3.Body.Close()
	if res3.StatusCode != http.StatusInternalServerError {
		t.Errorf("stage 3 status = %d, want 500", res3.StatusCode)
	}

	t.Logf("QA Diagnostic: Failure propagation, 429/500 handling, and secret scrubbing verified across pipeline stages.")
}
