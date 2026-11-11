package model

import "testing"

func TestValidateCrossModalChainThreePipelines(t *testing.T) {
	// Chain 1: Text -> Image -> Vision QA
	chain1 := []CrossModalStep{
		{StageName: "prompt_refine", InputModality: "text", OutputModality: "text", ContentType: "application/json", ArtifactPayload: "A red lighthouse", SessionID: "sess_1"},
		{StageName: "image_gen", InputModality: "text", OutputModality: "image", ContentType: "image/png", ArtifactPayload: "data:image/png;base64,iVBORw0KGgo=", SessionID: "sess_1"},
		{StageName: "vision_qa", InputModality: "image", OutputModality: "text", ContentType: "application/json", ArtifactPayload: "Verified red lighthouse", SessionID: "sess_1"},
	}
	if err := ValidateCrossModalChain(chain1); err != nil {
		t.Fatalf("expected valid chain1, got: %v", err)
	}

	// Broken handoff: Image output fed into Audio-only input
	broken := []CrossModalStep{
		chain1[1],
		{StageName: "stt", InputModality: "audio", OutputModality: "text", ContentType: "application/json", ArtifactPayload: "txt", SessionID: "sess_1"},
	}
	if err := ValidateCrossModalChain(broken); err == nil {
		t.Fatal("expected error for broken modality handoff")
	}
}
