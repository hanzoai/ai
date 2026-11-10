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
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/ai/stt"
	"github.com/hanzoai/ai/tts"
)

// TestQA133_TTSFormatsVoicesSpeedsAndContainerTruthfulness verifies text-to-speech
// (/v1/audio/speech) request serialization across voices, speeds, and output formats
// (mp3, wav, opus, aac, flac, pcm), as well as audioResponseLabel and audioExtension
// container truthfulness when upstream providers report actual MIME types vs fallbacks.
func TestQA133_TTSFormatsVoicesSpeedsAndContainerTruthfulness(t *testing.T) {
	voices := []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer", "george"}
	speeds := []float64{0.25, 0.5, 1.0, 1.25, 2.0, 4.0}
	formats := []struct {
		reqFormat string
		wantMime  string
		wantFile  string
	}{
		{"mp3", "audio/mpeg", "speech.mp3"},
		{"wav", "audio/wav", "speech.wav"},
		{"opus", "audio/opus", "speech.opus"},
		{"aac", "audio/aac", "speech.aac"},
		{"flac", "audio/flac", "speech.flac"},
		{"", "audio/mpeg", "speech.mp3"},
	}

	for _, v := range voices {
		for _, sp := range speeds {
			for _, f := range formats {
				raw, err := json.Marshal(audioSpeechRequest{
					Model:          "tts-1-hd",
					Input:          "Testing audio synthesis across voices, speeds, and formats.",
					Voice:          v,
					ResponseFormat: f.reqFormat,
					Speed:          sp,
				})
				if err != nil {
					t.Fatalf("marshal audioSpeechRequest(%s, %v, %s): %v", v, sp, f.reqFormat, err)
				}
				var decoded audioSpeechRequest
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Fatalf("unmarshal audioSpeechRequest: %v", err)
				}
				if decoded.Voice != v || decoded.Speed != sp || decoded.ResponseFormat != f.reqFormat {
					t.Fatalf("decoded = %+v, want voice=%s speed=%v format=%s", decoded, v, sp, f.reqFormat)
				}

				gotMime, gotFile := audioResponseLabel("", f.reqFormat)
				if gotMime != f.wantMime || gotFile != f.wantFile {
					t.Errorf("audioResponseLabel(\"\", %q) = (%q, %q), want (%q, %q)",
						f.reqFormat, gotMime, gotFile, f.wantMime, f.wantFile)
				}
			}
		}
	}

	// Upstream-reported MIME type must take precedence over requested format so
	// an upstream fallback (e.g. returning MP3 when Opus was requested) is never
	// mislabeled.
	actualOverrides := []struct {
		actual    string
		requested string
		wantMime  string
		wantFile  string
	}{
		{"audio/mpeg", "opus", "audio/mpeg", "speech.mp3"},
		{"audio/mp3", "wav", "audio/mp3", "speech.mp3"},
		{"audio/wav", "mp3", "audio/wav", "speech.wav"},
		{"audio/x-wav", "mp3", "audio/x-wav", "speech.wav"},
		{"audio/wave", "mp3", "audio/wave", "speech.wav"},
		{"audio/opus", "mp3", "audio/opus", "speech.opus"},
		{"audio/ogg", "mp3", "audio/ogg", "speech.opus"},
		{"audio/aac", "mp3", "audio/aac", "speech.aac"},
		{"audio/flac", "mp3", "audio/flac", "speech.flac"},
		{"audio/x-flac", "mp3", "audio/x-flac", "speech.flac"},
		{"audio/pcm", "mp3", "audio/pcm", "speech.pcm"},
		{"application/octet-stream", "pcm", "application/octet-stream", "speech.pcm"},
		{"audio/unknown-custom", "mp3", "audio/unknown-custom", "speech"},
	}
	for _, tc := range actualOverrides {
		gotMime, gotFile := audioResponseLabel(tc.actual, tc.requested)
		if gotMime != tc.wantMime || gotFile != tc.wantFile {
			t.Errorf("audioResponseLabel(%q, %q) = (%q, %q), want (%q, %q)",
				tc.actual, tc.requested, gotMime, gotFile, tc.wantMime, tc.wantFile)
		}
	}
}

// TestQA133_TTSMultilingualCharacterMeteringAndBounds verifies ttsCharsOf rune
// counting across multilingual scripts (English, CJK, Arabic, Hindi, Cyrillic, emoji)
// and MaxSpeechInput (4096-byte) enforcement on the ZAP speech handler.
func TestQA133_TTSMultilingualCharacterMeteringAndBounds(t *testing.T) {
	samples := []struct {
		lang      string
		text      string
		wantRunes int
	}{
		{"en", "Hello world, synthesizing clear speech.", 39},
		{"ja", "日本語の音声合成と文字起こしの精度テスト", 20},
		{"zh", "跨语言语音合成与长音频转录质量验证", 17},
		{"ar", "اختبار جودة تحويل النص إلى كلام", 31},
		{"hi", "बहुभाषी ऑडियो जनरेशन परीक्षण", 28},
		{"ru", "Проверка синтеза речи и транскрибации", 37},
		{"emoji", "Voice test 🎙️🔊🎧", 15},
	}

	for _, s := range samples {
		if got := ttsCharsOf(nil, s.text); got != s.wantRunes {
			t.Errorf("ttsCharsOf(nil, %s) = %d, want %d runes", s.lang, got, s.wantRunes)
		}
		if got := ttsCharsOf(&tts.TextToSpeechResult{TokenCount: 0}, s.text); got != s.wantRunes {
			t.Errorf("ttsCharsOf(TokenCount=0, %s) = %d, want %d runes", s.lang, got, s.wantRunes)
		}
		if got := ttsCharsOf(&tts.TextToSpeechResult{TokenCount: 128}, s.text); got != 128 {
			t.Errorf("ttsCharsOf(TokenCount=128, %s) = %d, want 128", s.lang, got)
		}
	}

	// Exact MaxSpeechInput (4096 bytes) passes the length guard (then fails auth with 401),
	// whereas MaxSpeechInput + 1 (4097 bytes) is rejected immediately with 413.
	ctx := context.Background()
	exactBody, _ := json.Marshal(audioSpeechRequest{
		Model: "tts-1",
		Input: strings.Repeat("a", MaxSpeechInput),
	})
	msgExact, err := zapAudioSpeechHandler(ctx, "Bearer invalid-test-token", exactBody)
	statusExact, _ := cloudStatus(t, msgExact, err)
	if statusExact == 413 {
		t.Fatalf("input at exact MaxSpeechInput (%d bytes) was rejected with 413", MaxSpeechInput)
	}

	oversizeBody, _ := json.Marshal(audioSpeechRequest{
		Model: "tts-1",
		Input: strings.Repeat("a", MaxSpeechInput+1),
	})
	msgOver, err := zapAudioSpeechHandler(ctx, "Bearer invalid-test-token", oversizeBody)
	statusOver, errText := cloudStatus(t, msgOver, err)
	if statusOver != 413 || !strings.Contains(errText, "exceeds the 4096 character limit") {
		t.Fatalf("oversize speech input got status=%d err=%q, want 413 limit error", statusOver, errText)
	}
}

// TestQA133_STTMultilingualMultipartAndTimestampGranularities verifies multipart
// transcription parsing across multilingual hints, audio containers, and both
// bracketed and bare timestamp_granularities fields.
func TestQA133_STTMultilingualMultipartAndTimestampGranularities(t *testing.T) {
	languages := []string{"en", "es", "fr", "de", "ja", "zh", "ar", "hi", "pt"}
	containers := []string{"sample.wav", "sample.mp3", "sample.webm", "sample.ogg", "sample.flac", "sample.m4a"}
	responseFormats := []string{"json", "verbose_json", "text"}

	for i, lang := range languages {
		container := containers[i%len(containers)]
		respFmt := responseFormats[i%len(responseFormats)]

		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("model", "whisper-1")
		_ = w.WriteField("language", lang)
		_ = w.WriteField("response_format", respFmt)
		_ = w.WriteField("timestamp_granularities[]", "word")
		_ = w.WriteField("timestamp_granularities", "segment")

		fw, err := w.CreateFormFile("file", container)
		if err != nil {
			t.Fatal(err)
		}
		audioBytes := []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80>\x00\x00\x00}\x00\x00\x02\x00\x10\x00data\x00\x00\x00\x00")
		if _, err := fw.Write(audioBytes); err != nil {
			t.Fatal(err)
		}
		_ = w.Close()

		form, err := parseTranscribeForm(buf.Bytes())
		if err != nil {
			t.Fatalf("parseTranscribeForm(lang=%s, file=%s): %v", lang, container, err)
		}
		if form.model != "whisper-1" || form.language != lang || form.responseFormat != respFmt {
			t.Errorf("parsed metadata = (%q, %q, %q), want (whisper-1, %q, %q)",
				form.model, form.language, form.responseFormat, lang, respFmt)
		}
		if len(form.timings) != 2 || form.timings[0] != stt.Word || form.timings[1] != stt.Segment {
			t.Errorf("parsed timings = %v, want [word segment]", form.timings)
		}
		gotAudio, _ := io.ReadAll(form.audio)
		if !bytes.Equal(gotAudio, audioBytes) {
			t.Errorf("audio payload mismatch for %s", container)
		}
	}

	// Unsupported timestamp_granularities inside multipart form must return an explicit error.
	var badBuf bytes.Buffer
	bw := multipart.NewWriter(&badBuf)
	_ = bw.WriteField("model", "whisper-1")
	_ = bw.WriteField("timestamp_granularities[]", "syllable")
	_ = bw.Close()
	if _, err := parseTranscribeForm(badBuf.Bytes()); err == nil || !strings.Contains(err.Error(), "unknown timestamp_granularities") {
		t.Fatalf("expected invalid granularity error from parseTranscribeForm, got %v", err)
	}
}

// TestQA133_WordAndSegmentTimestampOrderingAndVerboseJSON verifies word-level and
// segment-level timestamp serialization, monotonic interval ordering, and duration
// metering in verbose_json vs plain json outputs.
func TestQA133_WordAndSegmentTimestampOrderingAndVerboseJSON(t *testing.T) {
	transcript := &stt.Transcript{
		Text: "Latency p99 is 42ms across Kubernetes pods.",
		Words: []stt.TimedWord{
			{Word: "Latency", Start: 0.00, End: 0.48},
			{Word: "p99", Start: 0.48, End: 0.82},
			{Word: "is", Start: 0.82, End: 0.96},
			{Word: "42ms", Start: 0.96, End: 1.40},
			{Word: "across", Start: 1.40, End: 1.78},
			{Word: "Kubernetes", Start: 1.78, End: 2.45},
			{Word: "pods.", Start: 2.45, End: 2.90},
		},
		Segments: []stt.TimedSegment{
			{Id: 0, Start: 0.00, End: 1.40, Text: "Latency p99 is 42ms"},
			{Id: 1, Start: 1.40, End: 2.90, Text: "across Kubernetes pods."},
		},
	}
	res := &stt.SpeechToTextResult{AudioDurationSeconds: 2.90}
	dur := sttSecondsOf(res)
	if dur != 2.90 {
		t.Fatalf("sttSecondsOf = %v, want 2.90", dur)
	}

	rawVerbose := transcriptionBody("verbose_json", transcript, dur)
	var decoded transcriptionResponse
	if err := json.Unmarshal(rawVerbose, &decoded); err != nil {
		t.Fatalf("unmarshal verbose_json: %v", err)
	}
	if decoded.Task != "transcribe" || decoded.Duration != 2.90 || decoded.Text != transcript.Text {
		t.Fatalf("verbose_json header = %+v", decoded)
	}
	if len(decoded.Words) != 7 || len(decoded.Segments) != 2 {
		t.Fatalf("expected 7 words and 2 segments, got %d words and %d segments", len(decoded.Words), len(decoded.Segments))
	}

	// Verify monotonic non-overlapping timestamps across words and segments.
	prevEnd := 0.0
	for i, w := range decoded.Words {
		if w.Start < prevEnd || w.End < w.Start {
			t.Errorf("word[%d] (%q) has non-monotonic interval [%v, %v] after %v", i, w.Word, w.Start, w.End, prevEnd)
		}
		prevEnd = w.End
	}
	prevSegEnd := 0.0
	for i, seg := range decoded.Segments {
		if seg.Start < prevSegEnd || seg.End < seg.Start {
			t.Errorf("segment[%d] has non-monotonic interval [%v, %v] after %v", i, seg.Start, seg.End, prevSegEnd)
		}
		prevSegEnd = seg.End
	}
}

// TestQA133_LongRecordingUploadBoundsAndConcurrentSpeechAdmission verifies the
// 25 MiB MaxTranscribeUpload boundary on fits() and zapAudioTranscribeHandler,
// plus concurrent speech/transcription admission and clean release under cancellation.
func TestQA133_LongRecordingUploadBoundsAndConcurrentSpeechAdmission(t *testing.T) {
	if !fits(make([]byte, MaxTranscribeUpload)) {
		t.Fatal("exact 25 MiB upload must fit within MaxTranscribeUpload")
	}
	if fits(make([]byte, MaxTranscribeUpload+1)) {
		t.Fatal("upload of 25 MiB + 1 byte must be rejected by fits()")
	}

	// zapAudioTranscribeHandler must reject > 25 MiB with 413 before parsing multipart.
	msg, err := zapAudioTranscribeHandler(context.Background(), "Bearer test-token", make([]byte, MaxTranscribeUpload+1))
	status, errMsg := cloudStatus(t, msg, err)
	if status != 413 || !strings.Contains(errMsg, "25 MiB limit") {
		t.Fatalf("oversize transcription upload got status=%d err=%q, want 413", status, errMsg)
	}

	// Per-org speech/transcription slot admission, ceiling enforcement, and
	// idempotent release on stream interruption.
	speechIdle(t)

	var releases []func()
	for i := 0; i < speechCeiling; i++ {
		org := "qa-audio-org-a"
		if i%2 == 1 {
			org = "qa-audio-org-b"
		}
		rel, refused := admitSpeech(org)
		if refused != nil {
			t.Fatalf("slot %d for %s unexpectedly refused: %v", i, org, refused)
		}
		releases = append(releases, rel)
	}

	// At full ceiling, an additional concurrent speech/transcription request must
	// be refused immediately with HTTP 429.
	if _, refused := admitSpeech("qa-audio-org-c"); refused == nil {
		t.Fatal("expected admission refusal at full speechCeiling, got nil")
	} else if statusOf(refused) != http.StatusTooManyRequests {
		t.Fatalf("refusal status = %d, want 429", statusOf(refused))
	}

	// Simulate an interrupted stream releasing its slot twice (idempotent release).
	releases[0]()
	releases[0]()

	// Now the waiting org can immediately acquire the freed slot.
	relAfterCancel, refusedAfterCancel := admitSpeech("qa-audio-org-c")
	if refusedAfterCancel != nil {
		t.Fatalf("expected admission after stream cancellation freed a slot, got: %v", refusedAfterCancel)
	}
	relAfterCancel()
	for _, r := range releases[1:] {
		r()
	}
}
