// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"context"
	"encoding/json"
	"testing"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// decodeCloud parses a MsgType-100 cloud response into (status, body, errText).
func antDecodeCloud(t *testing.T, msg *zap.Message) (uint32, []byte, string) {
	t.Helper()
	if msg == nil {
		t.Fatal("nil zap message")
	}
	root := msg.Root()
	return root.Uint32(object.CloudRespStatus), root.Bytes(object.CloudRespBody), root.Text(object.CloudRespError)
}

// decodeGateway parses a MsgType-200 gateway response into (status, body).
func antDecodeGateway(t *testing.T, msg *zap.Message) (uint32, []byte) {
	t.Helper()
	if msg == nil {
		t.Fatal("nil zap message")
	}
	root := msg.Root()
	return root.Uint32(0), root.Bytes(4)
}

func assertAnthropicError(t *testing.T, body []byte, wantType string) {
	t.Helper()
	var e AnthropicErrorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("body is not AnthropicErrorBody JSON: %v (%s)", err, string(body))
	}
	if e.Type != "error" {
		t.Fatalf("error envelope type = %q, want %q", e.Type, "error")
	}
	if e.Error.Type != wantType {
		t.Fatalf("error.type = %q, want %q (%s)", e.Error.Type, wantType, string(body))
	}
}

// ── Identity rejection parity (no DB / IAM needed) ───────────────────────────

func TestZapAnthropicMessages_MissingAuth(t *testing.T) {
	msg, err := zapAnthropicMessagesGateway(context.Background(), "", []byte(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	status, body := antDecodeGateway(t, msg)
	if status != 401 {
		t.Fatalf("status = %d, want 401", status)
	}
	assertAnthropicError(t, body, "authentication_error")
}

func TestZapAnthropicMessages_PublishableKeyRejected(t *testing.T) {
	msg, err := zapAnthropicMessagesGateway(context.Background(), "Bearer pk-live-xyz", []byte(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	status, body := antDecodeGateway(t, msg)
	if status != 403 {
		t.Fatalf("status = %d, want 403", status)
	}
	assertAnthropicError(t, body, "auth_error")
}

// A bad body from an UNRESOLVABLE credential must be 401 (auth first), never a
// probe-able 400 — parity with the beego handler's ordering. An sk-shaped token is
// neither an hk- key nor a JWT, so zapResolveUser rejects it without any DB call.
func TestZapAnthropicMessages_AuthBeforeBadBody(t *testing.T) {
	msg, err := zapAnthropicMessagesGateway(context.Background(), "Bearer sk-not-a-real-key", []byte(`{not json`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	status, body := antDecodeGateway(t, msg)
	if status != 401 {
		t.Fatalf("status = %d, want 401 (auth must win over bad body)", status)
	}
	assertAnthropicError(t, body, "authentication_error")
}

// The cloud (MsgType 100) adapter mirrors the same triple, with the message also
// surfaced in the cloud error field.
func TestZapAnthropicMessages_CloudTransportError(t *testing.T) {
	msg, err := zapAnthropicMessagesCloud(context.Background(), "", []byte(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	status, body, errText := antDecodeCloud(t, msg)
	if status != 401 {
		t.Fatalf("status = %d, want 401", status)
	}
	if errText == "" {
		t.Fatal("cloud error field is empty, want the error message")
	}
	assertAnthropicError(t, body, "authentication_error")
}

func TestZapAnthropicCountTokens_MissingAuth(t *testing.T) {
	msg, err := zapAnthropicCountTokensGateway(context.Background(), "", []byte(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	status, body := antDecodeGateway(t, msg)
	if status != 401 {
		t.Fatalf("status = %d, want 401", status)
	}
	assertAnthropicError(t, body, "authentication_error")
}

func TestZapAnthropicCountTokens_PublishableKeyRejected(t *testing.T) {
	msg, err := zapAnthropicCountTokensGateway(context.Background(), "Bearer pk-abc", []byte(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	status, body := antDecodeGateway(t, msg)
	if status != 403 {
		t.Fatalf("status = %d, want 403", status)
	}
	assertAnthropicError(t, body, "auth_error")
}

// ── Happy-path token math parity (the count_tokens compute step, isolated) ───
//
// The full handler gates on zapResolveUser (IAM/DB); the only non-auth logic it
// runs is the token computation over the translated messages. Assert that compute
// path directly so a "happy" count is covered without infra.
func TestZapAnthropicCountTokens_ComputeParity(t *testing.T) {
	req := &AnthropicRequest{
		Model:     "claude-3-5-sonnet",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Hello, how many tokens is this sentence?"`)},
		},
	}
	msgs := anthropicToOpenAIMessages(req)
	if len(msgs) == 0 {
		t.Fatal("translation produced no messages")
	}
	n, err := model.OpenaiNumTokensFromMessages(msgs, req.Model)
	if err == nil && n <= 0 {
		t.Fatalf("token count = %d, want > 0", n)
	}
	// Fallback path must also yield a positive count (mirrors the handler).
	if n <= 0 {
		chars := 0
		for _, m := range msgs {
			chars += len(m.Content)
		}
		n = chars / 4
	}
	if n <= 0 {
		t.Fatalf("final token count = %d, want > 0", n)
	}
}
