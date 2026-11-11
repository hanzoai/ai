package model

import (
	"errors"
	"fmt"
)

// VideoGenerationJob represents a text-to-video or image-to-video job and its output telemetry.
type VideoGenerationJob struct {
	JobID             string
	Status            string
	RequestedDuration int
	ActualDuration    int
	RequestedRes      string
	ActualRes         string
	RequestedAspect   string
	ActualAspect      string
	FPS               int
	ErrorReason       string
}

var supportedResolutions = map[string]bool{"720p": true, "1080p": true, "4k": true}
var supportedAspects = map[string]bool{"16:9": true, "9:16": true, "1:1": true}

// ValidateVideoGenerationJob checks resolution/aspect/duration adherence and ensures failed jobs expose a clear reason.
func ValidateVideoGenerationJob(job VideoGenerationJob) error {
	if job.JobID == "" {
		return errors.New("missing_video_job_id")
	}
	if !supportedResolutions[job.RequestedRes] {
		return fmt.Errorf("unsupported_resolution: %s", job.RequestedRes)
	}
	if !supportedAspects[job.RequestedAspect] {
		return fmt.Errorf("unsupported_aspect_ratio: %s", job.RequestedAspect)
	}
	if job.Status == "failed" {
		if job.ErrorReason == "" {
			return errors.New("silent_video_job_failure_missing_error_reason")
		}
		return nil
	}
	if job.Status == "completed" {
		if job.ActualRes != job.RequestedRes {
			return fmt.Errorf("resolution_mismatch: requested=%s actual=%s", job.RequestedRes, job.ActualRes)
		}
		if job.ActualAspect != job.RequestedAspect {
			return fmt.Errorf("aspect_ratio_mismatch: requested=%s actual=%s", job.RequestedAspect, job.ActualAspect)
		}
		if job.ActualDuration <= 0 || job.ActualDuration != job.RequestedDuration {
			return fmt.Errorf("duration_mismatch: requested=%d actual=%d", job.RequestedDuration, job.ActualDuration)
		}
		if job.FPS < 12 {
			return fmt.Errorf("low_video_fps: %d", job.FPS)
		}
	}
	return nil
}
