package model

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ParsePNGDimensions extracts exact pixel width and height from a PNG image header (IHDR).
func ParsePNGDimensions(data []byte) (int, int, error) {
	if len(data) < 24 {
		return 0, 0, errors.New("png_data_too_short")
	}
	pngSig := []byte{0x89, 'P', 'N', 'G', '', '
', 0x1a, '
'}
	for i := range pngSig {
		if data[i] != pngSig[i] {
			return 0, 0, errors.New("invalid_png_signature")
		}
	}
	width := int(binary.BigEndian.Uint32(data[16:20]))
	height := int(binary.BigEndian.Uint32(data[20:24]))
	return width, height, nil
}

// ValidateImageGenerationConsistency checks that generated PNG dimensions match the requested size.
func ValidateImageGenerationConsistency(requestedWidth, requestedHeight int, pngBytes []byte) error {
	w, h, err := ParsePNGDimensions(pngBytes)
	if err != nil {
		return err
	}
	if w != requestedWidth || h != requestedHeight {
		return fmt.Errorf("image_dimension_mismatch: requested=%dx%d actual=%dx%d",
			requestedWidth, requestedHeight, w, h)
	}
	return nil
}
