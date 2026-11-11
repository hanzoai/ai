package model

import "testing"

func TestValidateTextEdgeCaseResponse(t *testing.T) {
	if err := ValidateTextEdgeCaseResponse(true, 100, 42, "stop", `{"needle":"found"}`, []string{"call_1", "call_2"}); err != nil {
		t.Fatalf("expected valid edge-case response, got: %v", err)
	}
	if err := ValidateTextEdgeCaseResponse(true, 50, 50, "stop", `{"partial":true}`, nil); err == nil {
		t.Fatal("expected error when truncated response did not report finish_reason=length")
	}
}
