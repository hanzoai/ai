// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// panelWithin fails the test if fn does not return inside d. The pre-fix pileup
// regression held panelState.mu across the judge HTTP self-call, so a judge that
// re-enters the lock self-deadlocks (sync.Mutex is not reentrant) and hangs — this must
// fail loudly instead of wedging the whole test binary.
func panelWithin(t *testing.T, d time.Duration, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s — panel lock held across judge scoring (pileup regression)", name, d)
	}
}

// TestPanelScoreLockFreeDuringJudging is the regression guard for Fix A: panelScore MUST
// NOT hold panelState.mu while a judge scores (an outbound HTTP self-call). We prove it
// by having the injected judge take the SAME lock (via PanelSnapshot) mid-score — pre-fix
// that self-deadlocks and hangs; post-fix the lock is free during Phase-1 scoring so it
// returns at once.
func TestPanelScoreLockFreeDuringJudging(t *testing.T) {
	resetPanel()
	prev := rawJudgeScore
	t.Cleanup(func() { rawJudgeScore = prev })
	rawJudgeScore = func(_ *judgeConfig, _, _, _, _ string) (float64, bool) {
		// Acquiring the panel lock here deadlocks iff panelScore holds it across us.
		_ = PanelSnapshot()
		return 0.7, true
	}
	panelWithin(t, 2*time.Second, "panelScore", func() {
		if _, _, ok := panelScore(&judgeConfig{}, []string{"a", "b"}, "t", "p", "r"); !ok {
			t.Error("panelScore should score with the lock free during judging")
		}
	})
}

// TestJudgeConcurrencyBounded is the regression guard for Fix B: concurrent judging is
// capped at judgeConcurrency PROCESS-WIDE, so a stalled completion path can never pile up
// unbounded judge self-calls (the ~78k-goroutine OOM). Every judge blocks inside
// rawJudgeScore; we launch far more concurrent panels than the bound and assert the peak
// number of judges in flight never exceeds judgeConcurrency.
func TestJudgeConcurrencyBounded(t *testing.T) {
	resetPanel()
	prev := rawJudgeScore
	t.Cleanup(func() { rawJudgeScore = prev })

	release := make(chan struct{})
	var inFlight, peak int64
	var peakMu sync.Mutex
	rawJudgeScore = func(_ *judgeConfig, _, _, _, _ string) (float64, bool) {
		n := atomic.AddInt64(&inFlight, 1)
		peakMu.Lock()
		if n > peak {
			peak = n
		}
		peakMu.Unlock()
		<-release // hold the slot until the test releases
		atomic.AddInt64(&inFlight, -1)
		return 0.5, true
	}

	var wg sync.WaitGroup
	for i := 0; i < judgeConcurrency*4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); panelScore(&judgeConfig{}, []string{"j"}, "t", "p", "r") }()
	}
	time.Sleep(200 * time.Millisecond) // let the goroutines race to fill the bound
	got := atomic.LoadInt64(&inFlight)
	close(release)
	wg.Wait()
	if got > judgeConcurrency {
		t.Fatalf("judges in flight = %d, exceeds bound %d", got, judgeConcurrency)
	}
	if peak > judgeConcurrency {
		t.Errorf("peak judges in flight = %d, exceeds bound %d", peak, judgeConcurrency)
	}
	if peak == 0 {
		t.Error("expected some judges to run")
	}
}

// TestJudgeSaturationAbstains is the graceful-degradation guard: when the judge bound is
// fully occupied, a new panel abstains (ok=false) immediately instead of blocking —
// judging sheds load rather than piling up another self-call.
func TestJudgeSaturationAbstains(t *testing.T) {
	resetPanel()
	for i := 0; i < judgeConcurrency; i++ { // saturate the process-wide bound
		judgeSem <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < judgeConcurrency; i++ {
			<-judgeSem
		}
	})
	panelWithin(t, 2*time.Second, "panelScore(saturated)", func() {
		if _, _, ok := panelScore(&judgeConfig{}, []string{"a", "b", "c"}, "t", "p", "r"); ok {
			t.Error("panel must abstain (ok=false) when the judge bound is saturated")
		}
	})
}
