// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/voice"
)

// A spoken conversation, served here rather than as a service of its own.
//
// hanzoai/voice holds the conversation and hanzoai/speech holds the models; what
// was missing was a host. This is the host: api.hanzo.ai already answers
// /v1/audio/speech and /v1/audio/transcriptions, and the realtime socket is the
// same surface with the turns joined up, so it belongs beside them behind one
// API host rather than behind a second one to deploy, route and operate.
//
// It lives in controllers, not routers, because a conversation is billed and
// usageRecord is written from here.

// voiceMeter bills a conversation to the tenant that held it.
//
// who.Org is the paying tenant as IAM resolved it, which is NOT the token's
// `owner` claim — owner names the application a token was minted through, so
// reading it as the tenant bills one org for another org's audio. voice does
// that resolution through IAM's own SDK; this only has to spend the answer.
type voiceMeter struct{}

func (voiceMeter) Bill(who voice.Who, heard, spoke time.Duration) {
	if who.Org == "" {
		return // nothing to bill to; better silent than billed to the wrong tenant
	}
	// Heard and spoken are both audio this served, so both are charged. The rate
	// is configured per model like every other row — an unpriced model lands
	// Unpriced rather than free, which is the same treatment the audio API gets.
	recordUsage(&usageRecord{
		Owner:        who.Org,
		Organization: who.Org,
		User:         who.User,
		Model:        voiceModel(),
		Provider:     "voice",
		Currency:     "USD",
		Status:       "success",
		AudioSeconds: (heard + spoke).Seconds(),
		RequestID:    uuid.NewString(),
	})
}

func voiceSetting(key, fallback string) string {
	if v := strings.TrimSpace(conf.GetConfigString(key)); v != "" {
		return v
	}
	return fallback
}

func voiceModel() string { return voiceSetting("VOICE_MODEL", "zen-omni") }

// VoiceHandler serves /v1/voice, or nil when there is no IAM to check a bearer
// against.
//
// Nil rather than an ungated socket: a WebSocket is exempt from the same-origin
// policy, so an unauthenticated one is any page on the internet opening a
// microphone session as whoever is signed in. Absent a gate the right surface is
// no surface, and the router simply answers 404 as it did before.
func VoiceHandler() http.Handler {
	issuer := strings.TrimRight(voiceSetting("IAM_URL", ""), "/")
	if issuer == "" {
		return nil
	}
	speech := voiceSetting("SPEECH_URL", "http://speech.hanzo.svc")

	v := &voice.Voice{
		Gate:   voice.NewGate(issuer+"/v1/iam/.well-known/jwks", []string{issuer}, nil),
		Desk:   voice.NewDesk(),
		Room:   voice.NewFloorspace(voice.Capacity, voice.Share),
		Speech: voice.NewSpeech(speech),
		Log:    slog.New(slog.NewJSONHandler(os.Stdout, nil)),
		Meter:  voiceMeter{},
		Model:  voiceSetting("VOICE_TTS", "kokoro"),
		Voice:  voiceSetting("VOICE_VOICE", "af_heart"),
		Think: func(w voice.Who) (voice.Mind, voice.Turn) {
			return &voice.Pipe{
				Speech: voice.NewSpeech(speech),
				STT:    voiceSetting("VOICE_STT", "whisper"),
				Prompt: voiceSetting("VOICE_PROMPT",
					"You are a voice assistant. Answer in one or two short sentences."),
				Chat: &voice.Chat{
					// Our own model surface. The caller's bearer and org travel
					// with the call so the thinking is billed to whoever spoke,
					// exactly as the audio is, and never to a service account.
					URL:   voiceSetting("VOICE_AI", "https://api.hanzo.ai"),
					Model: voiceModel(),
					HTTP:  &http.Client{},
					Token: w.Token,
					Org:   w.Org,
				},
			}, &voice.Hush{}
		},
	}
	if origins := voiceSetting("VOICE_ORIGINS", ""); origins != "" {
		v.Origins = strings.Split(origins, ",")
	}

	mux := http.NewServeMux()
	v.Routes(mux)
	return mux
}
