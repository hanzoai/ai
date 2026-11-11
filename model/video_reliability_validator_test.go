package model

import "testing"

func TestValidateVideoGenerationJob(t *testing.T) {
	okJob := VideoGenerationJob{
		JobID:             "vid_01",
		Status:            "completed",
		RequestedDuration: 5,
		ActualDuration:    5,
		RequestedRes:      "1080p",
		ActualRes:         "1080p",
		RequestedAspect:   "16:9",
		ActualAspect:      "16:9",
		FPS:               24,
	}
	if err := ValidateVideoGenerationJob(okJob); err != nil {
		t.Fatalf("expected valid video job, got: %v", err)
	}

	mismatch := okJob
	mismatch.ActualRes = "720p"
	if err := ValidateVideoGenerationJob(mismatch); err == nil {
		t.Fatal("expected error for silent resolution downgrade")
	}
}
