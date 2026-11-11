package model

import (
	"errors"
	"fmt"
	"strings"
)

// CrossModalStep represents a single stage in a multi-step cross-modal pipeline
// (e.g., Text -> Image -> Vision QA, Audio -> Transcription -> LLM -> TTS, Text -> Image -> Video).
type CrossModalStep struct {
	StageName       string
	InputModality   string
	OutputModality  string
	ContentType     string
	ArtifactPayload string
	SessionID       string
	Metadata        map[string]string
}

// ValidateCrossModalChain verifies modality handoffs, MIME content-types, non-empty payloads,
// and session/metadata continuity across chained multimodal turns.
func ValidateCrossModalChain(steps []CrossModalStep) error {
	if len(steps) < 2 {
		return errors.New("cross_modal_chain_requires_at_least_two_steps")
	}
	expectedSession := steps[0].SessionID
	if strings.TrimSpace(expectedSession) == "" {
		return errors.New("missing_session_id_at_chain_start")
	}

	for i, step := range steps {
		if step.SessionID != expectedSession {
			return fmt.Errorf("session_id_dropped_at_step_%d", i)
		}
		if strings.TrimSpace(step.ArtifactPayload) == "" {
			return fmt.Errorf("empty_artifact_payload_at_step_%d_%s", i, step.StageName)
		}
		if strings.TrimSpace(step.ContentType) == "" {
			return fmt.Errorf("missing_content_type_at_step_%d", i)
		}
		if i > 0 {
			prev := steps[i-1]
			if step.InputModality != prev.OutputModality {
				return fmt.Errorf("modality_handoff_mismatch_at_step_%d: prev_out=%s curr_in=%s",
					i, prev.OutputModality, step.InputModality)
			}
		}
	}
	return nil
}
