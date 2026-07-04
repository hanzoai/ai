// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"strings"
	"sync"
	"time"
)

// Video generation is ASYNC (OpenAI Sora-shaped): POST /v1/videos/generations
// creates an upstream job and returns immediately; the client polls
// GET /v1/videos/{id} and downloads GET /v1/videos/{id}/content. This file is the
// ONE authority for a job's billing reservation across those three separate
// requests.
//
// The pod runs single-replica (replicas: min=max=1 — the balance ledger's in-pod
// invariant), so an in-memory store is the RIGHT store: the reservation created
// at POST time lives in the same process that later settles it on completion, and
// no external store could be more consistent with the in-pod balance ledger it
// settles against. On pod restart in-flight jobs are lost — the client re-polls,
// gets a clean 404, and re-creates; acceptable for a minutes-long best-effort
// generation, and no money is lost (an unsettled reservation is released on
// restart because the ledger is in-pod too).

const (
	// maxOutstandingVideoJobs bounds the in-pod job map so a flood of create calls
	// can't grow it without limit. At the ceiling, create returns 429.
	maxOutstandingVideoJobs = 256

	// videoJobTTL is how long a NON-terminal job may live before the reaper
	// releases its reservation and evicts it. Upstream generation is ~60-300s, so
	// 15m is a generous ceiling that still frees a wedged/abandoned job's held
	// budget rather than leaking it forever.
	videoJobTTL = 15 * time.Minute

	// videoJobRetention is how long a TERMINAL (completed/failed) job is kept after
	// it finished, so a client can still fetch /content (and re-poll) once the job
	// is done. Its reservation is already settled, so this only bounds map size.
	videoJobRetention = 30 * time.Minute

	// videoReapInterval is the reaper's sweep cadence.
	videoReapInterval = time.Minute
)

// videoJob carries everything needed to (a) re-resolve the upstream provider and
// proxy its async video API for one job and (b) settle that job's per-video
// billing EXACTLY ONCE on completion. It is created at POST time and mutated
// (status/billing) under mu by the poll/download handlers.
type videoJob struct {
	// Immutable after creation.
	id         string      // our public OpenAI-shaped id ("video_<uuid>")
	upstreamID string      // the provider's job id (never returned to the client)
	subject    string      // billing subject — ALSO the ownership key (never empty)
	owner      string      // IAM owner (org) for the usage record
	name       string      // IAM user name for the usage record
	userModel  string      // requested model (e.g. "zen3-video") — re-resolves the provider
	isPremium  bool        // premium flag for the usage record
	hold       *budgetHold // balance reservation, settled exactly once
	createdAt  time.Time   // for the reaper TTL
	startTime  time.Time   // for the usage-trace latency

	mu            sync.Mutex
	status        string    // last observed status (queued|in_progress|completed|failed)
	failureReason string    // short, secret-scrubbed reason surfaced on a failed job
	billed        bool      // completion settled + usage recorded exactly once
	done          bool      // terminal (completed or failed) — reservation already settled
	terminalAt    time.Time // when the job became terminal (drives retention eviction)
}

// setFailureReason stores a short, already-scrubbed reason for a failed job so
// the response projection can surface it. Never carries a secret (the caller
// passes model.videoSafeReason output).
func (j *videoJob) setFailureReason(reason string) {
	if reason == "" {
		return
	}
	j.mu.Lock()
	j.failureReason = reason
	j.mu.Unlock()
}

// setStatus records the latest non-terminal upstream status.
func (j *videoJob) setStatus(status string) {
	j.mu.Lock()
	j.status = status
	j.mu.Unlock()
}

// snapshotStatus returns the last observed status for the response projection.
func (j *videoJob) snapshotStatus() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

// markCompleted settles the reservation with the ACTUAL video cost EXACTLY ONCE
// and reports whether this call was the first to do so — so the caller records
// the billable usage event once and never double-charges. Idempotent: a later
// poll or the /content download returns false. This is the single per-video
// debit point (the one debit authority): the reservation gate ran at create, and
// the ledger settle + Commerce usage record both fire here, together, once.
func (j *videoJob) markCompleted(costCents int64) (first bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status = "completed"
	if j.billed {
		return false
	}
	j.hold.settle(costCents)
	j.billed = true
	j.done = true
	j.terminalAt = time.Now()
	return true
}

// markFailed releases the reservation (a failed job bills NOTHING) EXACTLY ONCE
// and reports whether this call was the first to do so. Idempotent across repeat
// polls.
func (j *videoJob) markFailed(status string) (first bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if status == "" {
		status = "failed"
	}
	j.status = status
	if j.done {
		return false
	}
	j.hold.settle(0) // release the reservation; nothing is billed for a failed job
	j.billed = true
	j.done = true
	j.terminalAt = time.Now()
	return true
}

// videoJobStore is the in-pod registry of live video jobs.
type videoJobStore struct {
	mu   sync.Mutex
	jobs map[string]*videoJob
}

var videoJobs = &videoJobStore{jobs: make(map[string]*videoJob)}

// add registers a job, returning false when the pod is already tracking
// maxOutstandingVideoJobs (the caller then releases the reservation and answers
// 429). Bounds map growth against a create flood.
func (s *videoJobStore) add(j *videoJob) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) >= maxOutstandingVideoJobs {
		return false
	}
	s.jobs[j.id] = j
	return true
}

// get looks up a job by its public id.
func (s *videoJobStore) get(id string) (*videoJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

// reap releases the reservation of any non-terminal job older than videoJobTTL
// (a wedged/abandoned generation must not leak its held budget) and evicts any
// terminal job past its retention window. Called on videoReapInterval.
func (s *videoJobStore) reap(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		j.mu.Lock()
		if j.done {
			if now.Sub(j.terminalAt) > videoJobRetention {
				delete(s.jobs, id)
			}
		} else if now.Sub(j.createdAt) > videoJobTTL {
			// Abandoned / wedged: release the held budget and evict. markFailed's
			// invariants are simple enough to inline here under both locks.
			j.hold.settle(0)
			j.done = true
			j.billed = true
			j.status = "failed"
			delete(s.jobs, id)
		}
		j.mu.Unlock()
	}
}

var videoReaperOnce sync.Once

// startVideoReaperOnce launches the reaper goroutine the first time any video job
// is created. A single long-lived ticker sweeps the store; nothing to stop since
// the pod owns its whole lifetime.
func startVideoReaperOnce() {
	videoReaperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(videoReapInterval)
			defer ticker.Stop()
			for now := range ticker.C {
				videoJobs.reap(now)
			}
		}()
	})
}

// normalizeVideoStatus folds the upstream's status synonyms into the OpenAI Sora
// vocabulary the client sees: queued | in_progress | completed | failed. An
// unknown status is surfaced as-is (lowercased) so a new upstream state is never
// silently mistaken for a terminal one.
func normalizeVideoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "succeeded", "success":
		return "completed"
	case "failed", "error", "canceled", "cancelled":
		return "failed"
	case "in_progress", "processing", "running", "started":
		return "in_progress"
	case "queued", "pending", "created", "":
		return "queued"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

// isTerminalVideoStatus reports whether a normalized status is terminal.
func isTerminalVideoStatus(status string) bool {
	return status == "completed" || status == "failed"
}
