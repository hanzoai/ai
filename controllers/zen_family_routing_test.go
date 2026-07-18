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
	"testing"
)

// withModel reconciles a forwarded family body's "model" field with the model ai
// resolved — the fix for the auto→family 404: an `auto` request rewrites request.Model
// to a concrete SKU while the raw body still says "auto", and the family serves by the
// body's model field. It must set the model AND preserve every sibling field, and never
// drop a request on malformed input.
func TestWithModel(t *testing.T) {
	// The exact auto→enso case: body says "auto", resolved SKU is enso-flash.
	body := []byte(`{"model":"auto","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"temperature":0.7}`)
	out := withModel(body, "enso-flash")

	var got struct {
		Model    string  `json:"model"`
		MaxTok   int     `json:"max_tokens"`
		Temp     float64 `json:"temperature"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("withModel produced invalid JSON: %v", err)
	}
	if got.Model != "enso-flash" {
		t.Errorf("model = %q, want enso-flash", got.Model)
	}
	// Every sibling field is preserved losslessly.
	if got.MaxTok != 64 || got.Temp != 0.7 || len(got.Messages) != 1 || got.Messages[0].Content != "hi" {
		t.Errorf("withModel did not preserve sibling fields: %+v", got)
	}

	// A body with no model field gains one.
	if out := withModel([]byte(`{"max_tokens":8}`), "zen5"); !json.Valid(out) {
		t.Fatalf("withModel produced invalid JSON for a model-less body")
	} else {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(out, &m)
		if string(m["model"]) != `"zen5"` {
			t.Errorf("model-less body: model = %s, want \"zen5\"", m["model"])
		}
	}

	// Malformed input is returned unchanged — the rewrite never drops a request.
	bad := []byte(`not json`)
	if out := withModel(bad, "enso-flash"); string(out) != string(bad) {
		t.Errorf("malformed body should be returned unchanged, got %q", out)
	}
}

// familyRoutingText extracts the last user turn + media flag from a raw chat body for
// the shadow router — one parser for both dialects (string or typed-array content).
func TestFamilyRoutingText(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantText  string
		wantMedia bool
	}{
		{
			name:     "openai string content, last user turn wins",
			raw:      `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"why is the sky blue"}]}`,
			wantText: "why is the sky blue",
		},
		{
			name:     "anthropic system + messages",
			raw:      `{"system":"be terse","messages":[{"role":"user","content":"prove the theorem"}]}`,
			wantText: "prove the theorem",
		},
		{
			name:      "array content with image part → media, text joined",
			raw:       `{"messages":[{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"x"}}]}]}`,
			wantText:  "describe this",
			wantMedia: true,
		},
		{
			name:      "anthropic image block → media",
			raw:       `{"messages":[{"role":"user","content":[{"type":"image","source":{}},{"type":"text","text":"what is this"}]}]}`,
			wantText:  "what is this",
			wantMedia: true,
		},
		{
			name:     "no user turn → empty",
			raw:      `{"messages":[{"role":"assistant","content":"hi"}]}`,
			wantText: "",
		},
		{
			name:     "garbage body → empty, no panic",
			raw:      `not json`,
			wantText: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, media := familyRoutingText([]byte(tc.raw))
			if text != tc.wantText {
				t.Errorf("text=%q, want %q", text, tc.wantText)
			}
			if media != tc.wantMedia {
				t.Errorf("media=%v, want %v", media, tc.wantMedia)
			}
		})
	}
}

// sniffZenModel reads the served arm from a family response — top-level "model" or the
// Anthropic message.model — so the routing ledger records the concrete rung that ran.
func TestSniffZenModel(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"openai top-level model", `{"id":"chatcmpl-1","model":"enso","choices":[]}`, "enso"},
		{"anthropic message.model", `{"type":"message_start","message":{"model":"enso-ultra"}}`, "enso-ultra"},
		{"absent → empty", `{"id":"x","choices":[]}`, ""},
		{"garbage → empty", `nope`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sniffZenModel([]byte(tc.raw)); got != tc.want {
				t.Errorf("sniffZenModel=%q, want %q", got, tc.want)
			}
		})
	}
}

// sniffZenId reads the response id the client sees — the /v1/feedback join key.
func TestSniffZenId(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"openai chatcmpl id", `{"id":"chatcmpl-abc123","model":"enso","choices":[]}`, "chatcmpl-abc123"},
		{"anthropic message id (top)", `{"id":"msg_01xyz","type":"message"}`, "msg_01xyz"},
		{"anthropic message.id (stream start)", `{"type":"message_start","message":{"id":"msg_01xyz"}}`, "msg_01xyz"},
		{"absent → empty", `{"model":"enso","choices":[]}`, ""},
		{"garbage → empty", `nope`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sniffZenId([]byte(tc.raw)); got != tc.want {
				t.Errorf("sniffZenId=%q, want %q", got, tc.want)
			}
		})
	}
}
