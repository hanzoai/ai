// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// A message arrives in either of the two shapes the Anthropic API defines, and
// both have to read as the same text.
//
// The SDK sends a bare string for a simple turn and an array of blocks the moment
// anything else is attached, so a reader that understands only one of them drops
// the customer's words on whichever turn they attach a file to — silently, with a
// 200 and an answer to a prompt nobody wrote.
func TestAnthropicContentReadsBothShapes(t *testing.T) {
	for _, tc := range []struct {
		what string
		raw  string
		want string
	}{
		{"nothing at all", ``, ""},
		{"a bare string, which is most turns", `"hello"`, "hello"},
		{"an explicit null", `null`, ""},
		{"one text block", `[{"type":"text","text":"hello"}]`, "hello"},
		{"several blocks read as lines", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb"},
		{"a picture has no text to read", `[{"type":"image","source":{"type":"base64"}},{"type":"text","text":"describe this"}]`, "describe this"},
		{"an empty block says nothing", `[{"type":"text","text":""}]`, ""},
		{"a block of some other type", `[{"type":"tool_use","text":"ignored"}]`, ""},
		{"neither shape, kept rather than lost", `{"unexpected":"object"}`, `{"unexpected":"object"}`},
	} {
		t.Run(tc.what, func(t *testing.T) {
			m := &AnthropicMessage{Role: "user", Content: json.RawMessage(tc.raw)}
			if got := m.ContentText(); got != tc.want {
				t.Errorf("ContentText() = %q, want %q", got, tc.want)
			}
			// The system prompt is the same reading of the same two shapes, and it
			// is asserted here so the pair cannot drift into two answers.
			r := &AnthropicRequest{System: json.RawMessage(tc.raw)}
			if got := r.SystemText(); got != tc.want {
				t.Errorf("SystemText() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Media is FORWARDED, never flattened to text — so the question is only whether
// any block is something other than text.
//
// Flattening is what a text-only reader does, and for a picture it produces an
// empty string: the model is then asked to describe something it was never sent.
func TestMediaIsRecognisedSoItIsForwardedWhole(t *testing.T) {
	msg := func(raw string) AnthropicMessage {
		return AnthropicMessage{Role: "user", Content: json.RawMessage(raw)}
	}
	for _, tc := range []struct {
		what string
		msgs []AnthropicMessage
		want bool
	}{
		{"a bare string is text and nothing else", []AnthropicMessage{msg(`"hello"`)}, false},
		{"no messages", nil, false},
		{"empty content", []AnthropicMessage{msg(``)}, false},
		{"only text blocks", []AnthropicMessage{msg(`[{"type":"text","text":"hi"}]`)}, false},
		{"a picture", []AnthropicMessage{msg(`[{"type":"image","source":{}}]`)}, true},
		{"a document", []AnthropicMessage{msg(`[{"type":"document","source":{}}]`)}, true},
		{"a picture in a later turn", []AnthropicMessage{msg(`"hi"`), msg(`[{"type":"image","source":{}}]`)}, true},
		{"a block that names no type", []AnthropicMessage{msg(`[{"text":"hi"}]`)}, false},
		{"an array we cannot read", []AnthropicMessage{msg(`[{"type":`)}, false},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if got := requestHasMediaAnthropic(&AnthropicRequest{Messages: tc.msgs}); got != tc.want {
				t.Errorf("requestHasMediaAnthropic = %v, want %v", got, tc.want)
			}
		})
	}
}

// A refusal is typed by its STATUS, in Anthropic's own vocabulary, because the
// word is what tells a client whether to retry, to fix the request, or to go and
// find a credential.
func TestAnthropicWireTypeFollowsTheStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "invalid_request_error"},
		{http.StatusUnauthorized, "authentication_error"},
		{http.StatusForbidden, "permission_error"},
		{http.StatusNotFound, "not_found_error"},
		{http.StatusTooManyRequests, "rate_limit_error"},
		// "retry shortly", not "we are broken" — api_error sends somebody to file
		// a bug for a supply problem that clears on its own.
		{http.StatusServiceUnavailable, "overloaded_error"},
		{http.StatusInternalServerError, "api_error"},
		{http.StatusPaymentRequired, "api_error"},
	} {
		if got := anthropicErrorTypeForStatus(tc.status); got != tc.want {
			t.Errorf("status %d typed %q, want %q", tc.status, got, tc.want)
		}
	}

	// And an error carries its own status through, so the table is read once.
	if got := anthropicErrorType(&apiError{status: http.StatusTooManyRequests}); got != "rate_limit_error" {
		t.Errorf("a 429 error typed %q, want rate_limit_error", got)
	}
	// An error that states no status at all is read as an auth failure, which is
	// what statusOf answers for one: the unattributed refusals on this path are
	// the credential ones.
	if got := anthropicErrorType(errors.New("no status here")); got != "authentication_error" {
		t.Errorf("an untyped error typed %q, want authentication_error", got)
	}
}
