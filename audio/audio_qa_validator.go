package audio

import (
	"bytes"
	"errors"
	"fmt"
)

// TranscriptionSegment represents a word or segment timestamp returned by `/v1/audio/transcriptions`.
type TranscriptionSegment struct {
	Text  string
	Start float64
	End   float64
}

// ValidateTTSAudioContainer checks magic bytes for `mp3`, `wav`, `flac`, and `opus`/`ogg` outputs.
func ValidateTTSAudioContainer(format string, data []byte) error {
	if len(data) < 12 {
		return errors.New("truncated_or_empty_audio_payload")
	}
	switch format {
	case "wav":
		if !bytes.HasPrefix(data, []byte("RIFF")) {
			return errors.New("invalid_wav_riff_header")
		}
	case "flac":
		if !bytes.HasPrefix(data, []byte("fLaC")) {
			return errors.New("invalid_flac_header")
		}
	case "opus":
		if !bytes.HasPrefix(data, []byte("OggS")) {
			return errors.New("invalid_opus_ogg_header")
		}
	case "mp3":
		if !bytes.HasPrefix(data, []byte("ID3")) && !(data[0] == 0xFF && (data[1]&0xE0) == 0xE0) {
			return errors.New("invalid_mp3_frame_sync_or_id3")
		}
	}
	return nil
}

// ValidateTranscriptionTimestamps verifies monotonic non-negative word/segment timestamps.
func ValidateTranscriptionTimestamps(segments []TranscriptionSegment, audioDurationSec float64) error {
	if len(segments) == 0 {
		return errors.New("empty_transcription_segments")
	}
	prevEnd := 0.0
	for i, seg := range segments {
		if seg.Start < 0 || seg.End < seg.Start {
			return fmt.Errorf("invalid_segment_bounds_at_%d: start=%.3f end=%.3f", i, seg.Start, seg.End)
		}
		if seg.Start+0.05 < prevEnd {
			return fmt.Errorf("non_monotonic_timestamp_drift_at_%d", i)
		}
		if audioDurationSec > 0 && seg.End > audioDurationSec+0.5 {
			return fmt.Errorf("timestamp_exceeds_audio_duration_at_%d", i)
		}
		prevEnd = seg.End
	}
	return nil
}
