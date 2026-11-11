package model

import (
	"testing"
)

func TestValidateSSEStreamAcrossModelFamilies(t *testing.T) {
	validStream := []string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}
	if err := ValidateSSEStream(validStream); err != nil {
		t.Fatalf("expected valid SSE stream, got: %v", err)
	}

	truncatedStream := []string{
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"Partial"},"finish_reason":null}]}`,
	}
	if err := ValidateSSEStream(truncatedStream); err == nil {
		t.Fatal("expected error for truncated stream missing finish_reason and [DONE]")
	}
}

func TestValidateStandardErrorEnvelope(t *testing.T) {
	body := []byte(`{"error":{"message":"Invalid model parameter","type":"invalid_request_error","param":"model","code":"model_not_found"}}`)
	if err := ValidateStandardErrorEnvelope(404, body); err != nil {
		t.Fatalf("expected valid error envelope, got: %v", err)
	}
	if err := ValidateStandardErrorEnvelope(200, body); err == nil {
		t.Fatal("expected error when HTTP 200 is used for an error envelope")
	}
}
