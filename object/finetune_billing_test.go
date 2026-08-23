// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

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
	cents, err := MeterFinetuneGpuHours("acme/alice", 0, "nvidia-h200")
	if err != nil {
		t.Errorf("metering nothing answered %v", err)
	}
	if cents != 0 {
		t.Errorf("metering nothing billed %d cents", cents)
	}
}
