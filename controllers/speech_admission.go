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

// Admission control for the speech models (whisper, kokoro).
//
// Speech is the one model family this estate serves from its OWN capacity: a
// two-replica CPU deployment, no autoscaler and no upstream to absorb a burst.
// That makes it the one family where a request's COST is not bounded by what
// the caller paid, because the size gate bounds bytes and the byte-to-work ratio
// of compressed audio is unbounded — the same megabyte is minutes of PCM or
// hours of low-bitrate Opus. A caller cannot be charged into behaving either:
// these models are unpriced, so the balance gate admits and never debits.
//
// So the bound has to be on the WORK, and the only property of the work known
// before it starts is how much of it is already running. This ceiling is that
// bound: a fixed number of in-flight speech requests per process, and a refusal
// the moment it is full. Refusing is the point — a queue would convert a burst
// into latency for everyone and hold the memory of every waiting body while it
// did, which is the failure it exists to prevent.

// maxSpeechConcurrency is how many speech requests one process will have in
// flight at once. Two upstream replicas, each CPU-bound, so a small multiple
// keeps both busy with enough overlap to cover request setup and no more. It
// is per PROCESS, so the estate-wide ceiling is this times the replica count of
// this service — a bound, not a quota, and deliberately the loose half of the
// pair: the per-org REQUEST RATE is what stops one caller from owning all of
// it (ScopeRateLimit, service class "audio").
const maxSpeechConcurrency = 4

// speechSlots holds one token per in-flight speech request. A buffered channel
// is the whole mechanism: send to take a slot, receive to return it, and a
// non-blocking send is the ceiling test and the acquisition in one step, so a
// slot can never be counted without being held.
var speechSlots = make(chan struct{}, maxSpeechConcurrency)

// admitSpeech takes an in-flight slot, returning the release and whether one
// was free. It never blocks: a caller that cannot be served now is told so now.
// The release is idempotent-by-construction only if called once, so callers
// defer it immediately on success.
func admitSpeech() (release func(), ok bool) {
	select {
	case speechSlots <- struct{}{}:
		return func() { <-speechSlots }, true
	default:
		return nil, false
	}
}

// speechBusyMessage is what a refused caller is told. It names the condition
// and that it is transient, so a client retries rather than treating it as a
// permanent rejection of the request it sent.
const speechBusyMessage = "speech capacity is fully in use; retry shortly"
