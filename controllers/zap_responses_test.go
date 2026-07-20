// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestZapResponsesRegistered proves the group self-registers its native cloud
// method and gateway path from its own file (the strangler dispatch seam).
func TestZapResponsesRegistered(t *testing.T) {
	if zapResponsesCloud["responses.create"] == nil {
		t.Fatal("responses.create not registered in zapResponsesCloud")
	}
	if zapResponsesGateway["/v1/responses"] == nil {
		t.Fatal("/v1/responses not registered in zapResponsesGateway")
	}
}

// TestZapResponsesAuthRejection asserts the auth seam: no token → 401, and a
// publishable (read-only) key → 403, exactly like the beego Responses path.
func TestZapResponsesAuthRejection(t *testing.T) {
	cases := []struct {
		name string
		auth string
		want uint32
	}{
		{"empty", "", 401},
		{"publishable", "Bearer pk-live-abc123", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := zapResponsesHandler(context.Background(), tc.auth, []byte(`{"model":"zen4-coder","input":"hi"}`))
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			got := msg.Root().Uint32(object.CloudRespStatus)
			if got != tc.want {
				t.Fatalf("status = %d, want %d (err=%q)", got, tc.want, msg.Root().Text(object.CloudRespError))
			}
		})
	}
}

// TestZapResponsesBodyIsZstd covers the frame sniff used in place of the
// Content-Encoding header the native ZAP path does not carry.
func TestZapResponsesBodyIsZstd(t *testing.T) {
	if !responsesBodyIsZstd([]byte{0x28, 0xB5, 0x2F, 0xFD, 0x00}) {
		t.Fatal("zstd magic not detected")
	}
	if responsesBodyIsZstd([]byte(`{"model":"x"}`)) {
		t.Fatal("plain JSON misdetected as zstd")
	}
	if responsesBodyIsZstd([]byte{0x28}) {
		t.Fatal("short buffer misdetected as zstd")
	}
}

// TestZapResponsesStreamBodyParity is the happy-path terminal-encode test with
// a stubbed completion: given the intermediate Chat Completions JSON the object
// layer produces, the native streaming path must emit the SAME Responses SSE
// event sequence (created → text deltas → completed) with real token usage —
// the billing/streaming parity that matters for this high-risk group.
func TestZapResponsesStreamBodyParity(t *testing.T) {
	chatBody := []byte(`{
		"choices":[{"message":{"role":"assistant","content":"hello world"}}],
		"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}
	}`)
	req := &OpenAIResponsesRequest{Model: "zen4-coder", Stream: true}

	sse, err := responsesStreamBody(chatBody, req, map[string]string{})
	if err != nil {
		t.Fatalf("responsesStreamBody: %v", err)
	}
	out := string(sse)

	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.completed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("SSE missing %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, `"delta":"hello world"`) {
		t.Fatalf("SSE missing text delta\n%s", out)
	}

	// The completed event must carry the real token counts (metering parity).
	completed := lastEventData(t, out, "response.completed")
	usage := completed["response"].(map[string]interface{})["usage"].(map[string]interface{})
	if usage["input_tokens"].(float64) != 11 || usage["output_tokens"].(float64) != 3 || usage["total_tokens"].(float64) != 14 {
		t.Fatalf("usage = %#v, want input=11 output=3 total=14", usage)
	}
}

// TestZapResponsesNonStreamParity asserts the buffered (non-stream) encode
// yields the shared Responses resource shape with output text + usage.
func TestZapResponsesNonStreamParity(t *testing.T) {
	chatBody := []byte(`{
		"choices":[{"message":{"role":"assistant","content":"answer"}}],
		"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
	}`)
	req := &OpenAIResponsesRequest{Model: "zen4-coder"}

	out, err := openAIChatResponseToResponses(chatBody, req, map[string]string{})
	if err != nil {
		t.Fatalf("openAIChatResponseToResponses: %v", err)
	}
	var resource map[string]interface{}
	if err := json.Unmarshal(out, &resource); err != nil {
		t.Fatalf("unmarshal resource: %v", err)
	}
	if resource["object"] != "response" || resource["status"] != "completed" {
		t.Fatalf("resource envelope = %#v", resource)
	}
	usage := resource["usage"].(map[string]interface{})
	if usage["total_tokens"].(float64) != 7 {
		t.Fatalf("total_tokens = %v, want 7", usage["total_tokens"])
	}
	outputs := resource["output"].([]interface{})
	msg := outputs[0].(map[string]interface{})
	content := msg["content"].([]interface{})[0].(map[string]interface{})
	if content["text"] != "answer" {
		t.Fatalf("output text = %v, want answer", content["text"])
	}
}

// lastEventData returns the JSON payload of the last SSE event with the given
// type from a rendered event-stream body.
func lastEventData(t *testing.T, sse, event string) map[string]interface{} {
	t.Helper()
	var last map[string]interface{}
	found := false
	for _, block := range strings.Split(sse, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 || strings.TrimSpace(lines[0]) != "event: "+event {
			continue
		}
		data := strings.TrimPrefix(strings.TrimSpace(lines[1]), "data: ")
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("bad SSE data for %s: %v", event, err)
		}
		last = payload
		found = true
	}
	if !found {
		t.Fatalf("no %s event in SSE", event)
	}
	return last
}
