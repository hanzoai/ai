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
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/stt"
	"github.com/hanzoai/ai/tts"
)

// The audio catalogue: the user-facing model names /v1/audio/* serves, and which
// endpoint each one answers on. Declared here so the guards below enumerate the
// same set the route table must carry — adding a speech model to one without the
// other fails a test rather than shipping a name nothing can serve.
var (
	sttModels = []string{"whisper", "whisper-small"}
	ttsModels = []string{"kokoro"}
)

// TestAudioModelsAreRouted is the regression guard for the gap that left
// /v1/audio/transcriptions mounted, the speech service deployed and READY, and
// nothing whatsoever between them: the endpoint resolved `model` through the
// SAME route table as chat, and no audio model was in it. Every request died at
// "model is not available" before a single byte reached the pod, and /v1/models
// listed no whisper and no kokoro.
//
// An endpoint that cannot name a model it serves is not an endpoint.
func TestAudioModelsAreRouted(t *testing.T) {
	for _, name := range append(append([]string{}, sttModels...), ttsModels...) {
		route, ok := modelRoutes[name]
		if !ok {
			t.Errorf("audio model %q has no route: /v1/audio/* cannot resolve it and /v1/models will not list it", name)
			continue
		}
		if route.providerName == "" {
			t.Errorf("audio model %q routes to no provider", name)
		}
		if route.upstreamModel == "" {
			t.Errorf("audio model %q declares no upstream model id", name)
		}
	}
}

// TestAudioRoutesResolveToASeededProvider closes the second half of the same
// gap: a route naming a provider that no seed row creates resolves to nil, which
// every caller reads as "not configured". The route and the row are two edits in
// two files, so the link between them is asserted rather than remembered.
//
// The URL must carry /v1: these are direct relays (stt/openai.go and
// tts/openai.go append /audio/transcriptions and /audio/speech to it), the exact
// inverse of the family rows guarded in provider_seed_test.go.
func TestAudioRoutesResolveToASeededProvider(t *testing.T) {
	seeded := object.SeededModelProviders()
	for _, name := range append(append([]string{}, sttModels...), ttsModels...) {
		route, ok := modelRoutes[name]
		if !ok {
			continue // reported by TestAudioModelsAreRouted
		}
		row, isSeeded := seeded[route.providerName]
		if !isSeeded {
			t.Errorf("audio model %q routes to provider %q, which no seed row creates: it resolves to nil at every call",
				name, route.providerName)
			continue
		}
		if row.State != "Active" {
			t.Errorf("audio provider %q has State %q; a non-Active Model row resolves to nil (ModelProviderUsable)",
				row.Name, row.State)
		}
		if !strings.HasSuffix(strings.TrimRight(row.ProviderUrl, "/"), "/v1") {
			t.Errorf("audio provider %q has ProviderUrl %q; a direct relay must carry the /v1 root it appends paths to",
				row.Name, row.ProviderUrl)
		}
	}
}

// TestAudioProviderTypeIsServedByBothFactories asserts the seeded Type actually
// reaches a client on BOTH audio paths. This is what separated STT from TTS: the
// STT factory grew its OpenAI branch, the TTS factory never did, so the same
// provider row that transcribed could not synthesize — it fell through the
// factory as (nil, nil) and surfaced as "the TTS provider type: OpenAI is not
// supported". Hearing without speaking is half a voice agent.
func TestAudioProviderTypeIsServedByBothFactories(t *testing.T) {
	seeded := object.SeededModelProviders()

	for _, name := range sttModels {
		row := seeded[modelRoutes[name].providerName]
		p, err := stt.GetSpeechToTextProvider(row.Type, modelRoutes[name].upstreamModel, row.ClientSecret, row.ProviderUrl, row.Flavor)
		if err != nil {
			t.Errorf("stt factory for %q (type %q): %v", name, row.Type, err)
			continue
		}
		if p == nil {
			t.Errorf("stt factory returns nil for provider type %q: %q cannot be transcribed", row.Type, name)
		}
	}

	for _, name := range ttsModels {
		row := seeded[modelRoutes[name].providerName]
		p, err := tts.GetTextToSpeechProvider(row.Type, modelRoutes[name].upstreamModel, row.ClientId, row.ClientSecret, row.ProviderUrl, row.ApiVersion, 0, row.Currency, row.Flavor, "en")
		if err != nil {
			t.Errorf("tts factory for %q (type %q): %v", name, row.Type, err)
			continue
		}
		if p == nil {
			t.Errorf("tts factory returns nil for provider type %q: %q cannot be synthesized", row.Type, name)
		}
	}
}

// TestAudioModelsAreListed asserts the speech models reach /v1/models. They are
// not hidden: a caller cannot use a transcription endpoint whose model names are
// undiscoverable, and the in-call agent picks its model from this catalogue.
func TestAudioModelsAreListed(t *testing.T) {
	for _, name := range append(append([]string{}, sttModels...), ttsModels...) {
		if modelRoutes[name].hidden {
			t.Errorf("audio model %q is hidden from /v1/models; speech models must be discoverable", name)
		}
	}
}
