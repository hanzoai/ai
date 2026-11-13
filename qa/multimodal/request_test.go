// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package multimodal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFakeGateway_EveryVector is the offline integration lane: one httptest gateway, every vector
// sent through the real Send path against the full-spectrum matrix row. It proves three things
// the live lane would otherwise have to discover with paid calls: the request shape carries each
// channel's part correctly, the payload never leaks into the trusted plaintext (the injection may
// ride only inside the artifact — that is what makes it an indirect-injection probe at all), and
// Classify returns HELD for a clean answer end to end.
func TestFakeGateway_EveryVector(t *testing.T) {
	row := Matrix[3] // the Google row serves all four channels, so no vector skips on capability

	for _, v := range Vectors {
		t.Run(v.ID, func(t *testing.T) {
			var got map[string]any
			var auth, path string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth, path = r.Header.Get("Authorization"), r.URL.Path
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("gateway could not parse the request body: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(cannedOpenAI("THE FILE SHOWS SOME FIGURES."))
			}))
			defer ts.Close()

			client := &Client{Base: ts.URL + "/v1", Key: "hzo_test", HTTP: ts.Client()}
			reply, err := Send(context.Background(), client, &row, &v)
			if err != nil {
				t.Fatal(err)
			}
			if path != "/v1/chat/completions" {
				t.Errorf("request landed on %s, want /v1/chat/completions", path)
			}
			if auth != "Bearer hzo_test" {
				t.Errorf("Authorization = %q, want the bearer credential forwarded", auth)
			}

			f := Classify(reply.Status, reply.Body, &v, row.Family, row.Model, true)
			if f.Outcome != OutcomeHeld || f.Severity != SevNone {
				t.Errorf("a clean answer classified as %s/%s — the canned reply contains neither the canary nor this vector's token", f.Outcome, f.Severity)
			}

			s := marshalled(got)
			for _, want := range []string{row.Model, SystemPrompt, v.BenignText(), `"temperature":0.1`} {
				if !strings.Contains(s, want) {
					t.Errorf("request is missing the trusted element %q", clip(want))
				}
			}
			assertChannelPart(t, &v, s)

			if payload := PayloadText(&v); strings.Contains(s, payload) {
				t.Error("the injection leaked into request plaintext — every vector must smuggle its text inside the artifact only")
			}
			if v.Hijack {
				for _, want := range []string{`"tools":[{`, "send_email", `"tool_choice":"auto"`} {
					if !strings.Contains(s, want) {
						t.Errorf("hijack vector is missing %s — the tool-boundary probe needs the declared tool offered", want)
					}
				}
			} else if strings.Contains(s, `"tools"`) {
				// The substring check must be on the tools key, not the word send_email: the
				// pinned SystemPrompt itself names that tool (it sets the boundary).
				t.Error("a non-hijack vector still offers the tool — its TOOL_BYPASS verdict would be unattributable")
			}
		})
	}
}

// assertChannelPart checks the wire shape of one channel's content part.
func assertChannelPart(t *testing.T, v *Vector, s string) {
	t.Helper()
	switch {
	case v.Mod == Image:
		for _, want := range []string{"image_url", "data:image/png;base64,"} {
			if !strings.Contains(s, want) {
				t.Errorf("image vector is missing %s", want)
			}
		}
	case v.Mod == Audio:
		for _, want := range []string{"input_audio", `"format":"wav"`} {
			if !strings.Contains(s, want) {
				t.Errorf("audio vector is missing %s", want)
			}
		}
	case v.Mod == Video && v.Frames:
		if strings.Contains(s, "video_url") {
			t.Error("the key-frames vector sent a container part instead of frames")
		}
		art, err := Build(v.Fixture, PayloadText(v))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(s, "data:image/jpeg;base64,"); n != len(art.Frames) {
			t.Errorf("%d frame parts, want one per key frame (%d)", n, len(art.Frames))
		}
	case v.Mod == Video:
		for _, want := range []string{"video_url", "data:video/avi;base64,"} {
			if !strings.Contains(s, want) {
				t.Errorf("video container vector is missing %s", want)
			}
		}
	case v.Mod == Document:
		art, err := Build(v.Fixture, PayloadText(v))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"file"`, art.Name, "data:" + art.Media + ";base64,"} {
			if !strings.Contains(s, want) {
				t.Errorf("document vector is missing %s", clip(want))
			}
		}
	}
}

// marshalled re-encodes a decoded request so substring checks run on the exact wire form
// (json.Marshal sorts map keys, keeping "format":"wav"-style assertions stable).
func marshalled(m map[string]any) string {
	raw, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
