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

package object

import (
	"sync"
	"time"
)

// BalanceLedgerTTL is how long a cached Commerce balance is considered fresh.
// It matches the router gate's cache window so both read one consistent clock.
const BalanceLedgerTTL = 30 * time.Second

// ledgerEntry is the per-subject in-pod billing state.
type ledgerEntry struct {
	balanceCents  int64     // last balance fetched from Commerce
	reservedCents int64     // sum of holds for in-flight requests
	fetchedAt     time.Time // when balanceCents was last set
}

// BalanceLedger is the in-pod source of truth for a billing subject's SPENDABLE
// balance: the last-known Commerce balance MINUS outstanding reservations for
// in-flight requests. The router balance gate reserves an estimated (upper-bound)
// cost before a paid request runs; the debit path settles the real cost after.
//
// Reserve and Settle are atomic under one mutex, so concurrent requests for the
// same subject can neither double-spend the same balance nor drive it negative —
// each in-flight request's worst-case cost is held until it settles. Applying the
// actual spend locally on Settle also closes the stale-cache window: a balance
// read that is up to TTL old no longer over-grants, because every settled request
// has already decremented the cached balance.
type BalanceLedger struct {
	mu      sync.Mutex
	entries map[string]*ledgerEntry
	ttl     time.Duration
}

// GlobalBalanceLedger is the process-wide singleton shared by the router gate
// (reserve / balance cache) and the controller debit path (settle).
var GlobalBalanceLedger = NewBalanceLedger(BalanceLedgerTTL)

// NewBalanceLedger creates an empty ledger with the given freshness TTL.
func NewBalanceLedger(ttl time.Duration) *BalanceLedger {
	return &BalanceLedger{entries: make(map[string]*ledgerEntry), ttl: ttl}
}

// SetBalance records a freshly-fetched Commerce balance for a subject and resets
// the freshness clock. Outstanding reservations are PRESERVED — a fresh read of
// the upstream balance does not cancel in-flight holds.
func (l *BalanceLedger) SetBalance(subject string, cents int64) {
	if subject == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[subject]
	if e == nil {
		e = &ledgerEntry{}
		l.entries[subject] = e
	}
	e.balanceCents = cents
	e.fetchedAt = time.Now()
}

// Snapshot returns the cached balance, outstanding reservations, whether the
// cached balance is fresh (within TTL), and whether the subject is known.
func (l *BalanceLedger) Snapshot(subject string) (balance, reserved int64, fresh, known bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[subject]
	if e == nil {
		return 0, 0, false, false
	}
	return e.balanceCents, e.reservedCents, time.Since(e.fetchedAt) <= l.ttl, true
}

// Available returns balance - reserved for a subject, and whether the subject is
// known. A cold subject returns (0, false).
func (l *BalanceLedger) Available(subject string) (cents int64, known bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[subject]
	if e == nil {
		return 0, false
	}
	return e.balanceCents - e.reservedCents, true
}

// Reserve atomically holds estCents against the subject's available balance.
// It records the hold and returns true ONLY if the subject is known AND
// available (balance - reserved) >= estCents. A cold subject (no cached balance)
// cannot be reserved against — the caller must SetBalance first (fetch from
// Commerce), so the gate never reserves blind. estCents <= 0 holds nothing but
// still requires a known, non-overdrawn balance.
func (l *BalanceLedger) Reserve(subject string, estCents int64) bool {
	if estCents < 0 {
		estCents = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[subject]
	if e == nil {
		return false
	}
	if e.balanceCents-e.reservedCents < estCents {
		return false
	}
	e.reservedCents += estCents
	return true
}

// EvictIdle removes entries whose balance is older than maxIdle AND have no
// outstanding reservations, to bound memory. An entry with live holds is never
// evicted (that would lose an in-flight reservation).
func (l *BalanceLedger) EvictIdle(maxIdle time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for k, e := range l.entries {
		if e.reservedCents == 0 && now.Sub(e.fetchedAt) > maxIdle {
			delete(l.entries, k)
		}
	}
}

// Settle releases a prior reservation of estCents and applies the actual spend
// locally (balance -= actualCents) so subsequent reads reflect it immediately,
// without waiting for the async Commerce refresh. Pass the SAME estCents used at
// Reserve time. actualCents may be 0 (a failed/empty request that still reserved).
// Reserved never goes below zero (defensive against a double-settle).
func (l *BalanceLedger) Settle(subject string, estCents, actualCents int64) {
	if estCents < 0 {
		estCents = 0
	}
	if actualCents < 0 {
		actualCents = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[subject]
	if e == nil {
		return
	}
	e.reservedCents -= estCents
	if e.reservedCents < 0 {
		e.reservedCents = 0
	}
	e.balanceCents -= actualCents
}
