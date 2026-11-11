package model

import (
	"encoding/binary"
	"testing"
)

func buildMinimalPNGHeader(w, h uint32) []byte {
	buf := make([]byte, 24)
	copy(buf[:8], []byte{0x89, 'P', 'N', 'G', '', '
', 0x1a, '
'})
	copy(buf[12:16], []byte("IHDR"))
	binary.BigEndian.PutUint32(buf[16:20], w)
	binary.BigEndian.PutUint32(buf[20:24], h)
	return buf
}

func TestValidateImageGenerationConsistency(t *testing.T) {
	hdr := buildMinimalPNGHeader(1024, 1024)
	if err := ValidateImageGenerationConsistency(1024, 1024, hdr); err != nil {
		t.Fatalf("expected valid 1024x1024 PNG dimensions, got: %v", err)
	}
	if err := ValidateImageGenerationConsistency(1792, 1024, hdr); err == nil {
		t.Fatal("expected error on silent image dimension mismatch")
	}
}
