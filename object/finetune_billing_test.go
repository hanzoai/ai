// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// GPU time is sold by the hour and used by the second, so the conversion rounds
// UP: a job that ran at all costs at least a cent. Rounding the other way makes
// every short job free, and short jobs are the ones you can run many of.
func TestGpuSecondsCostAtLeastACent(t *testing.T) {
	const h100 = "nvidia-h100-80gb" // 350 cents an hour
	for _, c := range []struct {
		seconds int64
		want    int64
		why     string
	}{
		{0, 0, "no time is no charge"},
		{1, 1, "a single second still costs a cent"},
		{10, 1, "ten seconds is under a cent and charged one"},
		{3600, 350, "an hour is the hourly rate exactly"},
		{7200, 700, "two hours is twice it"},
		{1800, 175, "half an hour is half of it"},
		{3601, 351, "a second past the hour is the next cent"},
	} {
		if got := gpuSecondsCostCents(c.seconds, h100); got != c.want {
			t.Errorf("%ds of %s cost %d cents, want %d — %s", c.seconds, h100, got, c.want, c.why)
		}
	}

	// A negative duration is not a refund.
	for _, seconds := range []int64{-1, -3600, -1 << 40} {
		if got := gpuSecondsCostCents(seconds, h100); got != 0 {
			t.Errorf("%ds cost %d cents, want 0", seconds, got)
		}
	}
}

// Each card is billed at its own rate, and they are ordered the way the hardware
// is — a mistake here bills an H200 job at a 4090's price.
func TestEachCardIsBilledAtItsOwnRate(t *testing.T) {
	ladder := []string{
		"nvidia-rtx-4090", "nvidia-l40s", "nvidia-a100-40gb",
		"nvidia-a100-80gb", "nvidia-h100-80gb", "nvidia-h200",
	}
	last := int64(0)
	for _, card := range ladder {
		rate := gpuRateCents(card)
		if rate <= 0 {
			t.Errorf("%s bills %d cents an hour", card, rate)
		}
		if rate <= last {
			t.Errorf("%s bills %d, no more than the %d before it", card, rate, last)
		}
		last = rate
	}

	// A card nobody registered is billed at a rate, not at nothing — free GPU time
	// is the one answer that cannot be right.
	unknown := gpuRateCents("nvidia-something-new")
	if unknown <= 0 {
		t.Fatalf("an unregistered card bills %d cents an hour", unknown)
	}
	if unknown != GpuHourlyCents["nvidia-a100-80gb"] {
		t.Errorf("an unregistered card bills %d; the stated fallback is the A100-80GB rate %d",
			unknown, GpuHourlyCents["nvidia-a100-80gb"])
	}
}

// A job that used no GPU time is not a call to Commerce: there is nothing to
// bill, and an unconfigured Commerce must not turn that into an error.
func TestAJobThatUsedNoTimeIsNotBilled(t *testing.T) {
	cents, err := MeterFinetuneGpuHours("acme/alice", 0, "nvidia-h200", "")
	if err != nil {
		t.Errorf("metering nothing answered %v", err)
	}
	if cents != 0 {
		t.Errorf("metering nothing billed %d cents", cents)
	}
}

// Empty execution_id MUST be rejected for any billable run — never fall back to
// mutable owner/name or finetune:<subject>.
func TestFinetuneBillingRejectsEmptyExecutionID(t *testing.T) {
	_, err := MeterFinetuneGpuHours("acme/alice", 3600, "nvidia-h100-80gb", "")
	if err == nil {
		t.Fatal("expected error when executionID is empty for billable GPU-hours")
	}
	_, err = MeterFinetuneGpuHours("acme/alice", 3600, "nvidia-h100-80gb", "   ")
	if err == nil {
		t.Fatal("expected error when executionID is whitespace for billable GPU-hours")
	}
}

// Qualification Case 15 & 16a:
//   - Retrying metering for the same FinetuneJob sends the exact same immutable
//     execution_id / ref / Idempotency-Key so Commerce debits ONCE.
//   - Deleting and recreating a FinetuneJob with the exact same (Owner, Name)
//     mints a distinct ExecutionId and distinct billing ref so the second run
//     bills independently and never collides with the first.
func TestQualification15_RecreatedFinetuneJobDistinctBillingIDs(t *testing.T) {
	restore, err := UseMemoryDB(
		fmt.Sprintf("file:ft_recreate_%d?mode=memory&cache=shared", time.Now().UnixNano()),
		&FinetuneJob{},
	)
	if err != nil {
		t.Fatalf("UseMemoryDB: %v", err)
	}
	t.Cleanup(restore)

	// Simulated Commerce settlement ledger enforcing deduplication by Idempotency-Key/ref.
	var mu sync.Mutex
	settled := make(map[string]int64)
	var seenKeys []string
	var seenHeaders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Amount      int64  `json:"amount"`
			Ref         string `json:"ref"`
			RequestID   string `json:"requestId"`
			ExecutionID string `json:"executionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		idemKey := r.Header.Get("Idempotency-Key")
		if idemKey == "" || idemKey != body.Ref || body.Ref != body.RequestID {
			http.Error(w, "mismatched idempotency headers/ref", http.StatusBadRequest)
			return
		}
		mu.Lock()
		seenKeys = append(seenKeys, body.Ref)
		seenHeaders = append(seenHeaders, idemKey)
		if _, dup := settled[idemKey]; !dup {
			settled[idemKey] = body.Amount
		}
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("commerceEndpoint", srv.URL)
	t.Setenv("commerceToken", "test-token")

	// First fine-tune job with (acme, llama-run).
	job1 := &FinetuneJob{
		Owner:     "acme",
		Name:      "llama-run",
		CreatedBy: "alice",
		GpuType:   "nvidia-h100-80gb",
	}
	if _, err := AddFinetuneJob(job1); err != nil {
		t.Fatalf("AddFinetuneJob #1: %v", err)
	}
	if job1.ExecutionId == "" {
		t.Fatal("job1 missing auto-minted immutable ExecutionId")
	}

	// Meter job1 TWICE (simulating transient retry): must use identical immutable ref,
	// and Commerce must record only ONE settlement.
	for attempt := 1; attempt <= 2; attempt++ {
		cents, err := MeterFinetuneGpuHours("acme/alice", 3600, job1.GpuType, job1.ExecutionId)
		if err != nil {
			t.Fatalf("job1 meter attempt %d: %v", attempt, err)
		}
		if cents != 350 {
			t.Fatalf("job1 cents = %d, want 350", cents)
		}
	}

	// Delete and recreate a NEW fine-tune job with the EXACT same (Owner, Name).
	if ok, err := DeleteFinetuneJob("acme", "llama-run"); err != nil || !ok {
		t.Fatalf("DeleteFinetuneJob: ok=%v err=%v", ok, err)
	}
	job2 := &FinetuneJob{
		Owner:     "acme",
		Name:      "llama-run",
		CreatedBy: "alice",
		GpuType:   "nvidia-h100-80gb",
	}
	if _, err := AddFinetuneJob(job2); err != nil {
		t.Fatalf("AddFinetuneJob #2: %v", err)
	}
	if job2.ExecutionId == "" || job2.ExecutionId == job1.ExecutionId {
		t.Fatalf("recreated job reused ExecutionId: job1=%q job2=%q", job1.ExecutionId, job2.ExecutionId)
	}

	cents2, err := MeterFinetuneGpuHours("acme/alice", 3600, job2.GpuType, job2.ExecutionId)
	if err != nil {
		t.Fatalf("job2 meter: %v", err)
	}
	if cents2 != 350 {
		t.Fatalf("job2 cents = %d, want 350", cents2)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenKeys) != 3 {
		t.Fatalf("expected 3 HTTP attempts (2 for job1 + 1 for job2), got %d", len(seenKeys))
	}
	if seenKeys[0] != seenKeys[1] {
		t.Fatalf("retry of job1 changed billing ref: %q vs %q", seenKeys[0], seenKeys[1])
	}
	if seenKeys[2] == seenKeys[0] {
		t.Fatalf("recreated job2 collided with job1 billing ref: %q", seenKeys[2])
	}
	if seenKeys[0] == "finetune:acme/llama-run" || seenKeys[0] == "finetune:acme/alice" {
		t.Fatalf("billing ref used mutable owner/name or subject: %q", seenKeys[0])
	}
	if len(settled) != 2 {
		t.Fatalf("expected exactly 2 distinct settled debits (one per job execution), got %d (%v)", len(settled), settled)
	}
}
