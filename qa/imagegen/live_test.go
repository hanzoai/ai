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

package imagegen

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

// Small trace helpers: an 8-hex prompt/artifact fingerprint and plain ints
// keep the report detail lines readable without dumping whole bodies.
func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
func sha8(s string) string               { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:])[:8] }
func itoa(n int) string                  { return strconv.Itoa(n) }

// requireLive gates every lane in this file: HANZO_QA_LIVE=1 opts into the
// network, HANZO_API_KEY pays for it. Without either, the lane skips with its
// reason — never silently passes.
func requireLive(t *testing.T) *Client {
	t.Helper()
	if !LiveOpted() {
		t.Skip(LiveGateEnv + "=1 not set — skipping live image lanes")
	}
	if !Keyed() {
		t.Skip(KeyEnv + " not set — live lanes need a gateway key to pay for generations")
	}
	return NewClient()
}

// artifact pulls the actual pixels for one reply entry (download the url or
// decode the b64) so ValidateImage can judge bytes, not labels.
func artifact(ctx context.Context, c *Client, im GenImage) ([]byte, string, error) {
	if im.B64JSON != "" {
		raw, err := decodeB64(im.B64JSON)
		return raw, "b64_json", err
	}
	rep, err := c.Fetch(ctx, im.URL)
	if err != nil {
		return nil, im.URL, err
	}
	return rep.Body, im.URL, nil
}

// TestLiveResolutionAspect is the acceptance-criteria core: across the two
// supported fal image SKUs and square/landscape/portrait sizes, every reply
// must carry a downloadable artifact whose sniffed MIME and decoded
// dimensions EXACTLY match what was requested. A silent size ignore shows up
// here as a failed finding with both dimension pairs in the trace.
func TestLiveResolutionAspect(t *testing.T) {
	c := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()

	for _, model := range []string{ZenImage, ZenImageSDXL} {
		for _, tc := range []struct{ size string }{{"1024x1024"}, {"1792x1024"}, {"1024x1792"}} {
			t.Run(model+"/"+tc.size, func(t *testing.T) {
				wantW, wantH, _ := ParseSize(tc.size)
				rep, err := c.Generate(ctx, model, "a minimalist studio photo of a red apple on a white backdrop", 1, tc.size, "")
				if err != nil {
					live.Add("resolution", model+" "+tc.size, false, "request: "+err.Error())
					t.Fatalf("request failed: %v", err)
				}
				data, err := CheckGatewayReply(rep.Status, rep.Body)
				if err != nil {
					live.Add("resolution", model+" "+tc.size, false, "reply: "+err.Error())
					t.Fatalf("reply shape: %v", err)
				}
				raw, src, err := artifact(ctx, c, data[0])
				if err != nil {
					live.Add("resolution", model+" "+tc.size, false, "artifact fetch "+src+": "+err.Error())
					t.Fatalf("artifact fetch: %v", err)
				}
				mime, w, h, err := ValidateImage(raw, wantW, wantH)
				detail := model + " size=" + tc.size + " src=" + src + " bytes=" + itoa(len(raw))
				if err != nil {
					live.Add("resolution", model+" "+tc.size, false, detail+": "+err.Error())
					t.Errorf("%v (%s)", err, detail)
					return
				}
				live.Add("resolution", model+" "+tc.size, true, detail+", mime="+mime+", dims="+itoa(w)+"x"+itoa(h))
			})
		}
	}
}

// promptAdherenceCorpus: complex compositions whose plumbing (verbatim
// transit, valid artifact back) is testable offline-style even live; the
// semantic "did the model obey" half is left to the logged artifacts.
var promptAdherenceCorpus = []struct{ lane, prompt string }{
	{"spatial", "a red cube on top of a blue sphere, with a green cylinder to the left of both, and a yellow cone behind the sphere"},
	{"style", "an oil painting in the style of Dutch golden age masters: a candlelit desk with an open book, a brass compass, and a half-peeled lemon"},
	{"negative", "a portrait of an elderly lighthouse keeper with weathered skin and kind eyes; no hat, no beard, no background buildings"},
	{"multi-subject", "three cats — one orange tabby asleep, one black cat sitting upright, one grey kitten mid-pounce — arranged left to right on a wooden fence"},
}

// TestLivePromptAdherence proves the relay carries complex prompts intact and
// every one of them yields a valid decodable artifact (no silent empties).
func TestLivePromptAdherence(t *testing.T) {
	c := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()

	for _, p := range promptAdherenceCorpus {
		t.Run(p.lane, func(t *testing.T) {
			rep, err := c.Generate(ctx, ZenImage, p.prompt, 1, "1024x1024", "")
			if err != nil {
				live.Add("prompt-adherence", p.lane, false, err.Error())
				t.Fatalf("%v", err)
			}
			data, err := CheckGatewayReply(rep.Status, rep.Body)
			if err != nil {
				live.Add("prompt-adherence", p.lane, false, err.Error())
				t.Fatalf("%v", err)
			}
			raw, src, err := artifact(ctx, c, data[0])
			if err != nil {
				live.Add("prompt-adherence", p.lane, false, err.Error())
				t.Fatalf("%v", err)
			}
			mime, w, h, err := ValidateImage(raw, 1024, 1024)
			if err != nil {
				live.Add("prompt-adherence", p.lane, false, src+": "+err.Error())
				t.Fatalf("%v", err)
			}
			live.Add("prompt-adherence", p.lane, true, "artifact valid: "+src+" mime="+mime+" dims="+itoa(w)+"x"+itoa(h)+" prompt_sha="+sha8(p.prompt))
		})
	}
}

// TestLiveCharacterConsistency drives the sequential-generation lane: the
// same prompt requested twice plus an n=2 batch must all return valid,
// DISTINCT artifacts — the machine half of character consistency (stable
// identity across shots is judged by eye from the logged URLs).
func TestLiveCharacterConsistency(t *testing.T) {
	c := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()

	const persona = "the same cartoon fox detective in a trench coat and deerstalker hat, standing in Victorian London fog"
	seen := map[string]bool{}
	collect := func(raw []byte, src string) {
		if _, _, _, err := ValidateImage(raw, 0, 0); err != nil {
			live.Add("character-consistency", "artifact-valid", false, src+": "+err.Error())
			return
		}
		key := sha8(string(raw))
		if seen[key] {
			live.Add("character-consistency", "distinct-artifacts", false, "identical bytes across shots: "+src)
			return
		}
		seen[key] = true
		live.Add("character-consistency", "artifact-valid", true, src+" hash="+key)
	}

	for shot := 1; shot <= 2; shot++ {
		rep, err := c.Generate(ctx, ZenImage, persona, 1, "1024x1024", "")
		if err != nil {
			live.Add("character-consistency", "sequential", false, err.Error())
			t.Fatalf("shot %d: %v", shot, err)
		}
		data, err := CheckGatewayReply(rep.Status, rep.Body)
		if err != nil {
			live.Add("character-consistency", "sequential", false, err.Error())
			t.Fatalf("shot %d reply: %v", shot, err)
		}
		raw, src, err := artifact(ctx, c, data[0])
		if err != nil {
			live.Add("character-consistency", "sequential", false, err.Error())
			t.Fatalf("shot %d artifact: %v", shot, err)
		}
		collect(raw, src)
	}

	rep, err := c.Generate(ctx, ZenImage, persona, 2, "1024x1024", "")
	if err != nil {
		live.Add("character-consistency", "n=2-batch", false, err.Error())
		t.Fatalf("batch: %v", err)
	}
	data, err := CheckGatewayReply(rep.Status, rep.Body)
	if err != nil {
		live.Add("character-consistency", "n=2-batch", false, err.Error())
		t.Fatalf("batch reply: %v", err)
	}
	if len(data) != 2 {
		live.Add("character-consistency", "n-honored", false, itoa(len(data))+" images for n=2")
	} else {
		live.Add("character-consistency", "n-honored", true, "n=2 returned 2 entries")
	}
	for i, im := range data {
		raw, src, err := artifact(ctx, c, im)
		if err != nil {
			live.Add("character-consistency", "batch-artifact", false, err.Error())
			continue
		}
		collect(raw, src+"#"+itoa(i))
	}
	t.Log("judge identity across these artifacts:", len(seen), "distinct valid images")
}

// textRenderingPrompts pins short and multi-word strings — the typography
// lane. The machine-verifiable half is "valid decodable artifact at the
// requested size"; legibility of the rendered words rides the logged URLs.
var textRenderingPrompts = []struct{ check, prompt string }{
	{"short-string", `a bold neon sign on a brick wall that reads "HELLO"`},
	{"multi-word", `a vintage cafe sign in serif type reading "OPEN ALL NIGHT"`},
}

func TestLiveTextRendering(t *testing.T) {
	c := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()

	for _, tc := range textRenderingPrompts {
		t.Run(tc.check, func(t *testing.T) {
			rep, err := c.Generate(ctx, ZenImage, tc.prompt, 1, "1024x1024", "")
			if err != nil {
				live.Add("text-rendering", tc.check, false, err.Error())
				t.Fatalf("%v", err)
			}
			data, err := CheckGatewayReply(rep.Status, rep.Body)
			if err != nil {
				live.Add("text-rendering", tc.check, false, err.Error())
				t.Fatalf("%v", err)
			}
			raw, src, err := artifact(ctx, c, data[0])
			if err != nil {
				live.Add("text-rendering", tc.check, false, err.Error())
				t.Fatalf("%v", err)
			}
			if _, w, h, err := ValidateImage(raw, 1024, 1024); err != nil {
				live.Add("text-rendering", tc.check, false, src+": "+err.Error())
				t.Fatalf("%v", err)
			} else {
				live.Add("text-rendering", tc.check, true, src+" dims="+itoa(w)+"x"+itoa(h))
			}
		})
	}
}

// TestLiveResponseFormatSilentIgnore reproduces a documented parameter
// mismatch: the relay PARSES response_format (controllers/images_api.go) but
// never forwards it — fal answers with URLs, so a b64_json request must be
// observed, not assumed. The finding is recorded either way; only a hard
// gateway error fails the lane.
func TestLiveResponseFormatSilentIgnore(t *testing.T) {
	c := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()

	rep, err := c.Generate(ctx, ZenImage, "a small paper boat on a calm pond at dawn", 1, "", "b64_json")
	if err != nil {
		live.Add("param-ignore", "response_format=b64_json", false, err.Error())
		t.Fatalf("%v", err)
	}
	data, err := CheckGatewayReply(rep.Status, rep.Body)
	if err != nil {
		live.Add("param-ignore", "response_format=b64_json", false, err.Error())
		t.Fatalf("%v", err)
	}
	if data[0].B64JSON == "" && data[0].URL != "" {
		live.Document("param-ignore", "response_format=b64_json",
			"b64_json was requested but a URL came back: response_format is parsed and silently ignored (controllers/images_api.go never forwards it)")
		t.Log("confirmed: response_format silently ignored — url returned, artifact:", data[0].URL)
		return
	}
	live.Add("param-ignore", "response_format=b64_json", true, "b64_json honored end to end")
}

// TestLiveEditingDoor probes the acceptance criteria's editing/inpainting
// area against the surface that actually exists: the relay routes ONLY
// /v1/images/generations (routers/router.go), so the OpenAI edits door must
// answer 404 — inpainting workflows live upstream at fal, not here. A 2xx or
// a 5xx would both be findings; 404 is the documented truth.
func TestLiveEditingDoor(t *testing.T) {
	c := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rep, err := c.PostRaw(ctx, EditsPath, map[string]any{"model": ZenImage, "prompt": "replace the sky with a sunset"})
	if err != nil {
		live.Add("editing", "edits-door", false, err.Error())
		t.Fatalf("%v", err)
	}
	if rep.Status == 404 {
		live.Document("editing", "edits-door", "POST /v1/images/edits -> 404: no editing/inpainting door on the relay; only /v1/images/generations is routed")
		return
	}
	live.Add("editing", "edits-door", false, "expected 404 for the unrouted edits door, got HTTP "+itoa(rep.Status))
	t.Errorf("edits door answered HTTP %d, want 404 (router exposes only /images/generations)", rep.Status)
}
