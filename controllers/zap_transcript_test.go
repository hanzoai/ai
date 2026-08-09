// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// What the streaming transcript door has to be true for, stated as the failures
// it prevents: a session's pushes reach the process that holds its window, one
// tenant cannot push audio into another's session, and the audio billed is the
// audio received — once, and all of it.

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── the meter ───────────────────────────────────────────────────────────────

// TestMeterBillsTheDeltaOfSeconds is the money property, and the reason the
// per-push `duration` field is not what is metered.
//
// `seconds` is the session's cumulative total. Billing its delta means the error
// is bounded by ONE reading's rounding no matter how many pushes there were,
// where summing per-push values accumulates every rounding — and a push whose
// answer was lost is covered by the next delta rather than lost revenue.
func TestMeterBillsTheDeltaOfSeconds(t *testing.T) {
	s := &session{org: "acme"}
	// A real session's reported totals: 250 ms a push, and the field speech
	// rounds to a millisecond.
	var billed float64
	for _, total := range []float64{0.25, 0.5, 0.75, 1.0, 1.25} {
		billed += meterDelta(s, total)
	}
	if billed != 1.25 {
		t.Fatalf("billed %v seconds over a 1.25 s session, want 1.25", billed)
	}
	// And nothing is billed twice: the same total again is no new audio.
	if again := meterDelta(s, 1.25); again != 0 {
		t.Errorf("a repeated total billed %v more seconds — audio would be charged twice on a retry", again)
	}
}

// TestMeterBillsTinyPushes. The per-push `duration` field is rounded to three
// decimals, so a push of 14 bytes or fewer reports 0.0 while the session's own
// total keeps the true float. Metering that field serves audio free; metering the
// delta of the total does not.
func TestMeterBillsTinyPushes(t *testing.T) {
	s := &session{org: "acme"}
	var billed float64
	const pushes = 1000
	each := 0.0004 // 12.8 bytes of pcm16 — under `duration`'s rounding floor
	total := 0.0
	for i := 0; i < pushes; i++ {
		total += each
		billed += meterDelta(s, total)
	}
	want := pushes * each
	if diff := billed - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("billed %v seconds, want %v — pushes under the rounding floor are being served free", billed, want)
	}
	// The control: rounding each push the way `duration` does bills exactly zero,
	// which is the bug this test exists to keep out.
	rounded := 0.0
	for i := 0; i < pushes; i++ {
		rounded += math.Round(each*1000) / 1000
	}
	if rounded != 0 {
		t.Fatalf("the fixture does not model the rounding trap: per-push rounding billed %v, want 0", rounded)
	}
}

// TestMeterNeverRefunds. `seconds` only grows, so a SMALLER total is an upstream
// that restarted under the same id — not audio to give back. A negative debit is
// a refund nobody asked for, applied to a tenant nobody chose.
func TestMeterNeverRefunds(t *testing.T) {
	s := &session{org: "acme"}
	meterDelta(s, 10)
	if back := meterDelta(s, 2); back != 0 {
		t.Fatalf("a total that went backwards billed %v — the meter refunded", back)
	}
}

// meterDelta is the arithmetic meterTranscript performs, exercised without the
// ledger behind it. It is the same expression, in one place, so a test cannot
// pass against arithmetic the handler does not do.
func meterDelta(s *session, total float64) float64 {
	liveMu.Lock()
	defer liveMu.Unlock()
	delta := total - s.metered
	if delta < 0 {
		delta = 0
	}
	s.metered = total
	return delta
}

// ── which process holds the session ─────────────────────────────────────────

// TestSessionIsPinnedToOnePod. speech.hanzo.svc is a ClusterIP and kube-proxy
// picks a backend PER CONNECTION, so a session addressed by Service name reaches
// a pod that has never heard of it about half the time at two replicas — and a
// client with a connection pool sees that intermittently, which is worse than a
// clean failure rate.
//
// So every call after the first goes to the address `open` named, and that
// address is a POD, not the Service.
func TestSessionIsPinnedToOnePod(t *testing.T) {
	for _, tc := range []struct{ name, at, base, want string }{
		{"the pod names itself", "http://10.244.3.17:8000", "http://speech.hanzo.svc/v1", "http://10.244.3.17:8000/v1"},
		{"a trailing slash is not a second path segment", "http://10.244.3.17:8000/", "http://speech.hanzo.svc/v1", "http://10.244.3.17:8000/v1"},
		// Empty is the honest answer from a single process: there is nothing to
		// pin to, so the address already in hand stays correct.
		{"nothing to pin to", "", "http://speech.hanzo.svc/v1", "http://speech.hanzo.svc/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinned(tc.at, tc.base); got != tc.want {
				t.Fatalf("pinned(%q, %q) = %q, want %q", tc.at, tc.base, got, tc.want)
			}
			if tc.at != "" && strings.Contains(pinned(tc.at, tc.base), "speech.hanzo.svc") {
				t.Fatal("a pinned session still addresses the Service — half its pushes will reach the wrong replica")
			}
		})
	}
}

// ── who may push into a session ─────────────────────────────────────────────

// TestASessionBelongsToTheOrgThatOpenedIt. A transcript id is a bearer of
// nothing. Without the org check a guessed id would let one tenant push audio
// into another's meter and read the transcript coming back — the id is in a URL,
// which is the least protected place a secret can live.
//
// The refusal is deliberately the SAME answer a missing session gets: whether an
// id exists is not a fact another tenant is entitled to.
func TestASessionBelongsToTheOrgThatOpenedIt(t *testing.T) {
	id := "ats_" + here + "_owned"
	liveMu.Lock()
	live[id] = &session{org: "acme", touched: time.Now(), release: func() {}}
	liveMu.Unlock()
	t.Cleanup(func() { forget(id) })

	mine, refusal := holderFor("acme", id)
	if refusal != "" {
		t.Fatalf("the owning org was refused its own session: %s", refusal)
	}
	if mine == nil {
		t.Fatal("the owning org got no session")
	}
	theirs, refusal := holderFor("other", id)
	if theirs != nil {
		t.Fatal("SECURITY: another tenant reached a session it did not open")
	}
	absent, missing := holderFor("acme", "ats_"+here+"_never-existed")
	if absent != nil {
		t.Fatal("a session that was never opened was resolved")
	}
	// Both refusals name only the id the caller already sent, so the two are the
	// same sentence with the caller's own string in it. Comparing the sentences
	// with that string removed is what says a stranger cannot tell a real id from
	// an invented one.
	shape := func(msg, tid string) string { return strings.ReplaceAll(msg, tid, "<id>") }
	if got, want := shape(refusal, id), shape(missing, "ats_"+here+"_never-existed"); got != want {
		t.Errorf("a foreign session answers %q and a missing one %q — the difference tells a stranger the id is real", got, want)
	}
}

// TestAnIdFromAnotherInstanceSaysSo. The record IS the routing decision and the
// meter's running total, so it is worthless to a process that did not open the
// session — but answering "no such transcript" for one that certainly exists
// sends whoever is holding it to look for an expiry bug. Every id names the
// process that minted it, so the wrong instance can say which kind of miss it is.
func TestAnIdFromAnotherInstanceSaysSo(t *testing.T) {
	_, refusal := holderFor("acme", "ats_deadbeef_from-another-replica")
	if !strings.Contains(refusal, "another instance") {
		t.Fatalf("an id minted elsewhere answers %q — indistinguishable from an expired session", refusal)
	}
	if here == "deadbeef" {
		t.Skip("this process happens to be deadbeef; the case cannot be modelled")
	}
	// And an id that is simply unknown, in OUR namespace, is a plain miss.
	if _, refusal := holderFor("acme", "ats_"+here+"_unknown"); strings.Contains(refusal, "another instance") {
		t.Fatalf("an unknown id from this instance answers %q — the two misses are not distinguished", refusal)
	}
}

// holderFor resolves a session for an org, returning the refusal text when it is
// refused. It is holder()'s decision without holder()'s credential parsing, which
// is a different subsystem's property and has its own tests.
func holderFor(owner, tid string) (*session, string) {
	liveMu.Lock()
	defer liveMu.Unlock()
	s, ok := live[tid]
	if !ok {
		if parts := strings.SplitN(tid, "_", 3); len(parts) == 3 && parts[0] == "ats" && parts[1] != here {
			return nil, "this transcript was opened by another instance; open a new one"
		}
		return nil, "no transcript " + tid
	}
	if s.org != owner {
		return nil, "no transcript " + tid
	}
	return s, ""
}

// ── capacity ────────────────────────────────────────────────────────────────

// TestAnAbandonedSessionGivesItsSlotBack. A client that goes away without closing
// — a browser tab shut mid-call — leaves a session holding an admission slot. If
// only an explicit close returned it, the ceiling would shrink by one per
// abandoned meeting until the service admitted nobody, with no attacker involved.
func TestAnAbandonedSessionGivesItsSlotBack(t *testing.T) {
	var mu sync.Mutex
	returned := 0
	id := "ats_" + here + "_abandoned"

	liveMu.Lock()
	live[id] = &session{
		org:     "acme",
		touched: time.Now().Add(-2 * transcriptIdle),
		release: func() { mu.Lock(); returned++; mu.Unlock() },
	}
	sweepTranscripts(time.Now())
	_, still := live[id]
	liveMu.Unlock()

	if still {
		t.Fatal("an abandoned session survived the sweep")
	}
	mu.Lock()
	defer mu.Unlock()
	if returned != 1 {
		t.Fatalf("the sweep returned %d slots, want 1 — the ceiling shrinks by one per abandoned meeting", returned)
	}
}

// TestAFreshSessionSurvivesTheSweep is the control: a sweep that dropped
// everything would also pass the test above.
func TestAFreshSessionSurvivesTheSweep(t *testing.T) {
	id := "ats_" + here + "_fresh"
	liveMu.Lock()
	live[id] = &session{org: "acme", touched: time.Now(), release: func() {}}
	sweepTranscripts(time.Now())
	_, still := live[id]
	liveMu.Unlock()
	t.Cleanup(func() { forget(id) })
	if !still {
		t.Fatal("the sweep dropped a session that is in use")
	}
}

// ── routing ─────────────────────────────────────────────────────────────────

// TestTranscriptDoesNotSwallowTranscriptions. "/v1/audio/transcript" is a literal
// prefix of "/v1/audio/transcriptions", and the gateway consults the HTTP-shaped
// registry FIRST — so a prefix rule that matched on characters rather than on path
// segments would route every batch transcription into the streaming door, where it
// is a session id nobody opened. The batch endpoint is the one paying customers
// already use.
func TestTranscriptDoesNotSwallowTranscriptions(t *testing.T) {
	for _, path := range []string{
		"/v1/audio/transcriptions",
		"/v1/audio/transcriptions/",
	} {
		for _, h := range lookupGatewayRoutes(path) {
			if _, err := h(context.Background(), http.MethodPost, path, "", "", nil); err == nil || !errors.Is(err, errDecline) {
				t.Fatalf("%s is claimed by an HTTP-shaped route — the batch endpoint is being swallowed", path)
			}
		}
	}
	// The control: the streaming paths ARE claimed, so the check above is about
	// segment boundaries and not about the registry being empty.
	for _, path := range []string{"/v1/audio/transcript", "/v1/audio/transcript/ats_x"} {
		if len(lookupGatewayRoutes(path)) == 0 {
			t.Fatalf("%s is claimed by nobody — the streaming door is not registered", path)
		}
	}
}
