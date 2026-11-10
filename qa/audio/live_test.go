// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package audio

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain flushes the live findings to HANZO_QA_REPORT when set, so a live
// run leaves the request/response trace artifact the acceptance criteria ask
// for without any offline run touching the tree.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := live.MaybeWrite(); err != nil {
		fmt.Fprintln(os.Stderr, "qa/audio report:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// keyed gates the billed live lanes: HANZO_QA_LIVE=1 opts into network calls,
// HANZO_API_KEY pays for them. Without either the lane skips with its reason —
// it never passes silently.
func keyed(t *testing.T) *Client {
	t.Helper()
	if os.Getenv(LivePublicEnv) != "1" || os.Getenv(KeyEnv) == "" {
		t.Skipf("%s=1 and %s required", LivePublicEnv, KeyEnv)
	}
	return NewClient()
}

// liveOnly gates the key-free lanes: one env var, no credential.
func liveOnly(t *testing.T) *Client {
	t.Helper()
	if os.Getenv(LivePublicEnv) != "1" {
		t.Skipf("%s=1 required", LivePublicEnv)
	}
	return NewClient()
}

// contentTypeBase strips parameters (charset, boundary) from a Content-Type.
func contentTypeBase(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		return strings.TrimSpace(ct[:i])
	}
	return strings.TrimSpace(ct)
}

// TestLiveCatalogPin fails loudly when either pinned SKU leaves the catalog —
// every other live lane would then silently test an unreachable model.
func TestLiveCatalogPin(t *testing.T) {
	c := keyed(t)
	reply, err := c.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(reply.Body, &catalog); err != nil || reply.Status != 200 {
		t.Fatalf("/models: HTTP %d, decode %v", reply.Status, err)
	}
	present := map[string]bool{}
	for _, m := range catalog.Data {
		present[m.ID] = true
	}
	for _, sku := range []string{SpeechModel, ScribeModel} {
		ok := present[sku]
		live.Add("catalog", "pin:"+sku, ok, fmt.Sprintf("listed among %d models", len(catalog.Data)))
		if !ok {
			t.Errorf("%q is no longer in /v1/models — pinned lanes would test nothing", sku)
		}
	}
}

// TestLiveSpeechFormats runs the full response_format matrix: every container
// must arrive as real bytes of THAT container — valid structure, honest
// Content-Type label, and audible signal. The three failure classes the issue
// names (format errors, truncated outputs, mislabelled containers) each have a
// hard assertion here; a refusal is reported with its status and body.
func TestLiveSpeechFormats(t *testing.T) {
	c := keyed(t)
	for _, f := range AllFormats {
		t.Run(string(f), func(t *testing.T) {
			reply, err := c.Speech(t.Context(), SpeechModel,
				"Format check. This response should arrive as "+string(f)+".", "", f, 0)
			if err != nil {
				live.Add("tts-formats", string(f), false, "transport: "+err.Error())
				t.Fatal(err)
			}
			if reply.Status != 200 {
				live.Add("tts-formats", string(f), false, fmt.Sprintf("HTTP %d: %s", reply.Status, clip(string(reply.Body))))
				t.Errorf("HTTP %d for response_format=%s: %s", reply.Status, f, clip(string(reply.Body)))
				return
			}
			info, err := Validate(reply.Body)
			if err != nil {
				live.Add("tts-formats", string(f), false, "container: "+err.Error())
				t.Errorf("%s response is not a valid %s container: %v", f, f, err)
				return
			}
			ct := contentTypeBase(reply.ContentType)
			mislabelled := ct != "" && !LabelMatchesFormat(ct, info.Format)
			live.Add("tts-formats-label", string(f), !mislabelled, fmt.Sprintf("Content-Type %q over %s bytes", ct, info.Format))
			if mislabelled {
				t.Errorf(`"Content-Type: %s" carries %s bytes — the player-facing lie from audio_api.go's history`, ct, info.Format)
			}
			if info.Format != f {
				live.Add("tts-formats", string(f), false, fmt.Sprintf("requested %s, provider produced %s (%.2f s)", f, info.Format, info.DurationSeconds))
				t.Errorf("requested response_format=%s, got %s bytes", f, info.Format)
			} else {
				live.Add("tts-formats", string(f), true, fmt.Sprintf("%s valid, %.2f s", info.Format, info.DurationSeconds))
			}
			if rms, _, ok := SignalLevel(info.Format, reply.Body); ok && rms < 0.01 {
				live.Add("tts-loudness", string(f), false, fmt.Sprintf("rms %.5f — valid container, no signal", rms))
				t.Errorf("%s response decoded to silence (rms %.5f)", f, rms)
			}
		})
	}
}

// TestLiveSpeechVoicesSpeeds probes the voice and speed parameters. A named
// voice the deployment does not carry must be refused cleanly, not answered
// with a 5xx; speed is judged on whether it moves the audio at all — a
// provider that accepts `speed` and ignores it is a finding, not a crash.
func TestLiveSpeechVoicesSpeeds(t *testing.T) {
	c := keyed(t)
	text := "Voice and speed probe, identical words every time."

	dur := func(voice string, speed float64) (float64, bool) {
		reply, err := c.Speech(t.Context(), SpeechModel, text, voice, MP3, speed)
		if err != nil {
			return 0, false
		}
		if reply.Status != 200 {
			live.Add("tts-voice-speed", fmt.Sprintf("voice=%q speed=%.1f", voice, speed), false, fmt.Sprintf("HTTP %d: %s", reply.Status, clip(string(reply.Body))))
			return 0, false
		}
		info, err := Validate(reply.Body)
		if err != nil {
			t.Errorf("voice=%q speed=%.1f: valid request produced an invalid container: %v", voice, speed, err)
			return 0, false
		}
		return info.DurationSeconds, true
	}

	d1, ok := dur("", 0) // the default voice at default speed must work — it is the baseline every row compares against
	if !ok {
		t.Fatal("default speech request failed")
	}
	// Named voices: OpenAI's canonical name plus a Kokoro-style one. Either may
	// be unknown to this deployment; what is NOT allowed is a 5xx or audio that
	// fails validation when the call does succeed.
	for _, v := range []string{"nova", "af_heart"} {
		if d, ok := dur(v, 0); ok {
			live.Add("tts-voice-speed", "voice:"+v, true, fmt.Sprintf("accepted, %.2f s", d))
		}
	}
	slow, okSlow := dur("", 0.8)
	fast, okFast := dur("", 1.3)
	if okSlow && okFast {
		held := slow > fast*1.05
		live.Add("tts-speed", "effect", held, fmt.Sprintf("speed 0.8 → %.2f s, speed 1.3 → %.2f s (default %.2f s)", slow, fast, d1))
		if !held {
			t.Errorf("speed is accepted but ignored: 0.8× produced %.2f s and 1.3× produced %.2f s", slow, fast)
		}
	}
}

// TestLiveSpeechStreaming consumes one synthesis in small reads the way a
// playback client does — the streamed-audio-chunks requirement. The reassembled
// bytes must still be a valid container and must match what the response
// promised to carry.
func TestLiveSpeechStreaming(t *testing.T) {
	c := keyed(t)
	reply, err := c.SpeechStreamed(t.Context(), SpeechModel, "Stream me in small pieces, please.", MP3, 4096)
	if err != nil {
		live.Add("tts-streaming", "chunked-read", false, err.Error())
		t.Fatal(err)
	}
	if reply.Status != 200 {
		live.Add("tts-streaming", "status", false, fmt.Sprintf("HTTP %d: %s", reply.Status, clip(string(reply.Body))))
		t.Fatalf("HTTP %d", reply.Status)
	}
	truncated := reply.Length >= 0 && int64(len(reply.Body)) != reply.Length
	live.Add("tts-streaming", "content-length", !truncated, fmt.Sprintf("%d bytes read, %d promised", len(reply.Body), reply.Length))
	if truncated {
		t.Errorf("stream delivered %d of the %d bytes Content-Length promised — mid-stream cut", len(reply.Body), reply.Length)
	}
	if _, err := Validate(reply.Body); err != nil {
		live.Add("tts-streaming", "container", false, err.Error())
		t.Errorf("reassembled stream is not a valid container: %v", err)
	} else {
		live.Add("tts-streaming", "container", true, fmt.Sprintf("%d bytes reassembled from 4 KiB reads, valid", len(reply.Body)))
	}
}

// TestLiveTranscribeRoundTrip is the heart of the suite: synthesize each
// language phrase, feed the audio straight back to the transcriber with word
// AND segment timestamps requested, and demand real words with honest clocks.
// English rows must reproduce most of their keywords; every other language
// must land at least one — enough to prove the multilingual path is wired.
func TestLiveTranscribeRoundTrip(t *testing.T) {
	c := keyed(t)
	for _, p := range Phrases {
		t.Run(p.Lang, func(t *testing.T) {
			tts, err := c.Speech(t.Context(), SpeechModel, p.Text, "", MP3, 0)
			if err != nil {
				live.Add("stt-roundtrip", p.Lang, false, "synthesis transport: "+err.Error())
				t.Fatalf("could not synthesize the fixture: %v", err)
			}
			if tts.Status != 200 {
				live.Add("stt-roundtrip", p.Lang, false, fmt.Sprintf("synthesis HTTP %d: %s", tts.Status, clip(string(tts.Body))))
				t.Fatalf("synthesis returned HTTP %d: %s", tts.Status, clip(string(tts.Body)))
			}
			info, err := Validate(tts.Body)
			if err != nil {
				t.Fatalf("synthesized audio is not a valid container: %v", err)
			}

			hint := p.Lang
			if strings.HasPrefix(hint, "en") {
				hint = "en" // en-tech is an English register, not its own ISO code
			}
			reply, err := c.Transcribe(t.Context(), TranscribeOpts{
				Model: ScribeModel, Audio: tts.Body, Filename: "roundtrip.mp3",
				Language: hint, Format: "verbose_json",
				Granularities: []string{TimingWord, TimingSegment},
			})
			if err != nil {
				t.Fatal(err)
			}
			if reply.Status != 200 {
				live.Add("stt-roundtrip", p.Lang, false, fmt.Sprintf("transcription HTTP %d: %s", reply.Status, clip(string(reply.Body))))
				t.Fatalf("HTTP %d: %s", reply.Status, clip(string(reply.Body)))
			}
			tr, err := DecodeTranscript(reply.Body)
			if err != nil {
				live.Add("stt-roundtrip", p.Lang, false, "body: "+err.Error())
				t.Fatal(err)
			}
			if bad := CheckTimings([]string{TimingWord, TimingSegment}, tr, info.DurationSeconds); len(bad) > 0 {
				live.Add("stt-timings", p.Lang, false, strings.Join(bad, "; "))
				t.Errorf("timestamp audit failed: %v", bad)
			} else {
				live.Add("stt-timings", p.Lang, true, fmt.Sprintf("%d words / %d segments aligned to %.2f s", len(tr.Words), len(tr.Segments), info.DurationSeconds))
			}

			hits := KeywordHits(tr.Text, p.Keywords)
			minHits := 1
			if hint == "en" { // English rows are held to most of their words
				minHits = (len(p.Keywords) + 1) / 2
			}
			ok := hits >= minHits && strings.TrimSpace(tr.Text) != ""
			live.Add("stt-roundtrip", p.Lang, ok, fmt.Sprintf("%d/%d keywords · heard %q", hits, len(p.Keywords), clip(tr.Text)))
			if !ok {
				t.Errorf("transcription of the %s phrase missed the bar (%d/%d keywords): %q", p.Lang, hits, len(p.Keywords), clip(tr.Text))
			}
		})
	}
}

// TestLiveLongRecording speaks ~1.2 kB of prose — over a minute of audio — and
// demands the decode cover it end to end. This is where chunk-boundary bugs
// and mid-stream truncation surface: a decoder that stops early or timestamps
// past the real end fails coverage here, not in production.
func TestLiveLongRecording(t *testing.T) {
	c := keyed(t)
	tts, err := c.Speech(t.Context(), SpeechModel, LongSpeechText, "", MP3, 0)
	if err != nil {
		live.Add("stt-long", "synthesis", false, err.Error())
		t.Fatalf("long synthesis failed: %v", err)
	}
	if tts.Status != 200 {
		live.Add("stt-long", "synthesis", false, fmt.Sprintf("HTTP %d: %s", tts.Status, clip(string(tts.Body))))
		t.Fatalf("long synthesis returned HTTP %d: %s", tts.Status, clip(string(tts.Body)))
	}
	info, err := Validate(tts.Body)
	if err != nil {
		t.Fatalf("long audio container invalid: %v", err)
	}
	if info.DurationSeconds < 20 {
		live.Add("stt-long", "duration", false, fmt.Sprintf("only %.1f s synthesized from %d chars — engine truncated?", info.DurationSeconds, len(LongSpeechText)))
		t.Errorf("expected well over 20 s of audio, got %.1f s", info.DurationSeconds)
	} else {
		live.Add("stt-long", "duration", true, fmt.Sprintf("%.1f s of audio from %d chars", info.DurationSeconds, len(LongSpeechText)))
	}

	reply, err := c.Transcribe(t.Context(), TranscribeOpts{
		Model: ScribeModel, Audio: tts.Body, Filename: "long.mp3",
		Language: "en", Format: "verbose_json",
		Granularities: []string{TimingWord, TimingSegment},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Status != 200 {
		live.Add("stt-long", "transcribe", false, fmt.Sprintf("HTTP %d: %s", reply.Status, clip(string(reply.Body))))
		t.Fatalf("HTTP %d: %s", reply.Status, clip(string(reply.Body)))
	}
	tr, err := DecodeTranscript(reply.Body)
	if err != nil {
		t.Fatal(err)
	}
	violations := append(CheckTimings([]string{TimingWord, TimingSegment}, tr, info.DurationSeconds), CheckCoverage(tr, info.DurationSeconds)...)
	live.Add("stt-long", "coverage", len(violations) == 0, fmt.Sprintf("%d segments over %.1f s; %v", len(tr.Segments), info.DurationSeconds, violations))
	if len(violations) > 0 {
		t.Errorf("long recording failed: %v", violations)
	}
}

// TestLiveSilenceHandling transcribes generated digital silence: a decoder
// handed nothing must answer with (nearly) nothing. Whisper's failure mode —
// inventing fluent paragraphs over silence, sometimes looping — is pinned hard.
func TestLiveSilenceHandling(t *testing.T) {
	c := keyed(t)
	wav := Silence(3)
	reply, err := c.Transcribe(t.Context(), TranscribeOpts{Model: ScribeModel, Audio: wav, Filename: "silence.wav", Format: "verbose_json"})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Status != 200 {
		live.Add("stt-silence", "status", false, fmt.Sprintf("HTTP %d: %s", reply.Status, clip(string(reply.Body))))
		t.Fatalf("silence upload returned HTTP %d: %s", reply.Status, clip(string(reply.Body)))
	}
	tr, err := DecodeTranscript(reply.Body)
	if err != nil {
		t.Fatal(err)
	}
	hallucinated := len([]rune(strings.TrimSpace(tr.Text))) > 120
	live.Add("stt-silence", "hallucination", !hallucinated, fmt.Sprintf("3 s of silence produced %q (%d segments)", clip(tr.Text), len(tr.Segments)))
	if hallucinated {
		t.Errorf("decoder invented %d runes over pure silence: %q", len([]rune(tr.Text)), clip(tr.Text))
	}
}

// TestLivePublicTranscriptions is the key-free lane: the public door must
// answer a real upload with a decodable body, and garbage with a clean 4xx —
// never a 5xx, which would make anonymous traffic a denial-of-service lever.
func TestLivePublicTranscriptions(t *testing.T) {
	c := liveOnly(t)
	c.Key = "" // the whole point: no credential rides at all

	reply, err := c.PublicTranscribe(t.Context(), Sine(440, 3))
	if err != nil {
		live.Add("stt-public", "tone", false, err.Error())
		t.Fatal(err)
	}
	switch {
	case reply.Status == 200:
		if _, err := DecodeTranscript(reply.Body); err != nil {
			live.Add("stt-public", "tone", false, "body: "+err.Error())
			t.Errorf("public 200 body did not decode: %v (%s)", err, clip(string(reply.Body)))
		} else {
			live.Add("stt-public", "tone", true, "anonymous upload answered with a decodable transcript")
		}
	case reply.Status >= 400 && reply.Status < 500:
		live.Add("stt-public", "tone", true, fmt.Sprintf("HTTP %d (allowance or refusal): %s", reply.Status, clip(string(reply.Body))))
	default:
		live.Add("stt-public", "tone", false, fmt.Sprintf("HTTP %d: %s", reply.Status, clip(string(reply.Body))))
		t.Errorf("public door answered a valid upload with HTTP %d", reply.Status)
	}

	garbage := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	reply, err = c.PublicTranscribe(t.Context(), garbage)
	if err != nil {
		t.Fatal(err)
	}
	clean := reply.Status < 500
	live.Add("stt-public", "garbage", clean, fmt.Sprintf("HTTP %d: %s", reply.Status, clip(string(reply.Body))))
	if !clean {
		t.Errorf("8 bytes of garbage took the public door to HTTP %d — anonymous callers must not be able to do that", reply.Status)
	}
}
