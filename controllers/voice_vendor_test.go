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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/decimal"
)

// speechListing is two of OpenRouter's speech models as listed on 2026-10-16: one
// priced by the character, one free.
const speechListing = `{"data":[
{"id":"vendor/chat","name":"Vendor: Chat","created":1,"context_length":1000,"pricing":{"prompt":"0.000001","completion":"0.000002"},"architecture":{"input_modalities":["text"],"output_modalities":["text"]}},
{"id":"elevenlabs/eleven-v3","name":"ElevenLabs: Eleven v3","created":1791386272,"architecture":{"input_modalities":["text"],"output_modalities":["speech"]},"pricing":{"prompt":"0.00004","completion":"0"},"supported_voices":["george","sarah"]},
{"id":"fish-audio/s2.1-pro-free:free","name":"Fish Audio: S2.1 Pro (free)","created":1,"architecture":{"input_modalities":["text"],"output_modalities":["speech"]},"pricing":{"prompt":"0","completion":"0"}},
{"id":"google/lyria-3-pro-preview","name":"Google: Lyria 3 Pro Preview","created":1774907286,"context_length":1048576,"architecture":{"input_modalities":["text","image"],"output_modalities":["text","audio"]},"pricing":{"prompt":"0","completion":"0"}}
]}`

// A speech model is a SKU that speaks: priced by what each call costs and on both
// paid floors, its voices listed; a free one is free. A music model the vendor
// lists at no token price bills each call at what it states and is never free.
func TestASpeechModelIsAVoiceSKU(t *testing.T) {
	fees(t, map[string]string{"openrouter": "5.5", "hanzo": "5.5"})
	models, err := openrouterCatalog([]byte(speechListing))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]zenModel{}
	for _, m := range models {
		by[m.ID] = m
	}
	v3 := by["elevenlabs/eleven-v3"]
	if v3.Voice == nil || !v3.priced() || !v3.Variable || v3.MinTier != "paid" || strings.Join(v3.Outputs, ",") != "speech" || strings.Join(v3.Voice.Voices, ",") != "george,sarah" {
		t.Errorf("eleven-v3 = %+v", v3)
	}
	// 1,000 characters at $0.00004 = $0.04 at the vendor.
	if got := v3.Voice.voiceCeiling(1000); got.Cmp(decimal.MustParse("0.04")) != 0 {
		t.Errorf("1,000 characters reserve %s, want 0.04", got)
	}
	if free := by["fish-audio/s2.1-pro-free:free"]; free.Voice == nil || free.priced() {
		t.Errorf("the free voice = %+v", free)
	}
	if lyria := by["google/lyria-3-pro-preview"]; !lyria.Variable || !lyria.priced() || lyria.MinTier != "paid" {
		t.Errorf("lyria variable=%v priced=%v tier=%q, want a priced SKU on the paid floors", lyria.Variable, lyria.priced(), lyria.MinTier)
	}
}

// Speech asked of a vendor's voice is spoken under the vendor's id and billed at
// what the vendor says the generation cost, times the markup.
func TestSpeechIsSpokenAndBilledAtWhatItCost(t *testing.T) {
	fees(t, map[string]string{"openrouter": "5.5", "hanzo": "5.5"})
	models, err := openrouterCatalog([]byte(speechListing))
	if err != nil {
		t.Fatal(err)
	}
	var v3 zenModel
	for _, m := range models {
		if m.ID == "elevenlabs/eleven-v3" {
			v3 = m
		}
	}
	var mu sync.Mutex
	var asked map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/audio/speech":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(b, &asked)
			mu.Unlock()
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Header().Set("X-Generation-Id", "gen-7")
			_, _ = w.Write([]byte("ID3 fake mp3"))
		case "/v1/generation":
			_, _ = io.WriteString(w, `{"data":{"id":"gen-7","total_cost":0.00044}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	fam := spareFamily(t, srv.URL, "vendor/free:free")
	fam.byID[v3.ID] = v3
	t.Setenv("OPENROUTER_API_KEY", "k1")
	forgetKeys()
	t.Cleanup(forgetKeys)
	got := ledger(t)

	c := visit(http.MethodPost, "/v1/audio/speech")
	c.vendorVoice(v3, audioSpeechRequest{Model: v3.ID, Input: "Eleven characters", Voice: "george"}, &iam.User{Owner: "acme", Name: "alice"}, time.Now())
	if body := sent(c); body != "ID3 fake mp3" {
		t.Fatalf("answered %q", body)
	}
	mu.Lock()
	if asked["model"] != "elevenlabs/eleven-v3" || asked["voice"] != "george" || asked["input"] != "Eleven characters" {
		t.Errorf("the vendor was asked %v", asked)
	}
	mu.Unlock()
	// $0.00044 × 1.113025 = $0.000489731 billed; × 1.055 = $0.0004642 its cost.
	ev := got()
	if len(ev) != 1 || ev[0].USD != "0.000489731" || ev[0].CostUSD != "0.0004642" || ev[0].Model != v3.ID {
		t.Fatalf("debits %+v", ev)
	}
}
