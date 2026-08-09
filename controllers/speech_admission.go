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

import (
	"fmt"
	"strings"
	"sync"
	"time"

	metric "github.com/luxfi/metric"
)

// Admission control for the speech models (whisper, kokoro).
//
// Speech is the one model family this estate serves from its OWN capacity: a
// two-replica CPU deployment, no autoscaler and no upstream to absorb a burst.
// That makes it the one family where a request's COST is not bounded by what the
// caller paid, because the size check bounds bytes and the byte-to-work ratio of
// compressed audio is unbounded — the same megabyte is minutes of PCM or hours
// of low-bitrate Opus. A caller cannot be charged into behaving either: these
// models are unpriced, so the balance gate admits and never debits.
//
// So the bound has to be on the WORK, and the only property of the work known
// before it starts is how much of it is already running.
//
// The bound is per TENANT. A single global ceiling can only answer "is the
// service full", which is the wrong answer when one org's all-hands is what
// filled it: every other tenant is refused for a condition it did not create and
// cannot influence, and the org holding the capacity is never the one told to
// stop. So capacity is DIVIDED — an org may hold an equal share of the ceiling,
// and the share is the ceiling over the number of tenants competing for it. One
// org alone holds the whole thing; two hold half each; nothing is reserved away
// from a tenant that is not asking for it.
//
// COMPETING, not holding, and the difference is the whole mechanism. A tenant
// being refused holds nothing, so a division that counts only holders cannot see
// it: the org that filled the ceiling is the only contender, its share is
// therefore everything, and it retakes each slot it frees before the starved
// tenant is ever counted. That is not a corner case — measured under sustained
// contention between two tenants, the greedy one won every single admission
// (2003 of 2003). So a refusal is remembered for speechDemand and counted as what
// it is: a tenant asking for capacity it does not have. With demand counted, the
// same measurement splits 50/50.
//
// The division is applied at admission and never by preemption: a decode already
// running is not interrupted. An org that filled the ceiling before a second org
// arrived keeps what it holds and is refused its NEXT request, so a freed slot
// goes to the starved tenant rather than back to the one that took everything.
// The split converges in one request's time instead of instantly, which is the
// price of not killing work that is already half done.
//
// Refusing rather than queueing is the point: a queue turns a burst into latency
// for everyone and holds the memory of every waiting body while it does, which is
// the failure this exists to prevent. A refused caller is told immediately, in
// terms it can act on.
//
// WHOSE ceiling it is comes from the credential, and never from the wire. Every
// door resolves identity first and keys the share on what that identity proves:
// c.billingOrg(authUser) and authUser.Owner on the OpenAI-shaped routes — the
// same expression each of those already bills, so the tenant that pays for a call
// is the tenant whose share it spends — and c.GetOrg() / orgOf(who) on the legacy
// store-bound routes, which bill the store's owner but must not let the body
// decide whose capacity is consumed.
//
// Nothing a caller writes can move a request into another org's share. Over ZAP
// there is no header at all: the handler is given a credential and a body. Over
// HTTP, an X-Org-Id naming an org the principal does not belong to resolves back
// to their own — GetOrg answers a non-member with their home org, and billingOrg
// refuses the switch rather than redirecting the bill. Naming someone else's org
// can only ever spend your own share.
//
// This ceiling is process-local: it is exact while the ai plane runs one replica,
// and a second replica would enforce a second private copy of it. A limit shared
// across replicas needs shared state, and that trade is not worth its failure
// modes at one replica.
//
// Nothing else bounds these endpoints per tenant. The rate limiter
// (routers/ratelimit.go) runs as a filter on routers.App, and the ZAP gateway
// dispatches /v1/audio/* straight to the handler out of its own registry without
// reaching App — so on the transport cloud actually calls, this is the only
// per-tenant bound there is.

// speechCeiling is how many speech requests one process will have in flight at
// once, across every tenant. Two upstream replicas, each CPU-bound with two
// decode workers, so four keeps every worker busy with nothing queued behind it.
const speechCeiling = 4

// speechDemand is how long a refusal keeps counting as a tenant wanting capacity.
// It has to outlast a client's retry — immediate, or a second or two — without
// outlasting its interest, and being wrong either way costs a share that is one
// request stale.
const speechDemand = 5 * time.Second

var (
	speechMu sync.Mutex
	// speechHeld is the in-flight count per org. An org holding nothing is ABSENT
	// rather than zero, so the map cannot outgrow the ceiling.
	speechHeld = map[string]int{}
	// speechAsked is when each org was last refused: the only evidence a tenant
	// wants capacity it does not have.
	//
	// Holdings alone are not enough to divide by, and the failure is not subtle.
	// A tenant being refused holds nothing, so counting only holders makes it
	// invisible — the org that filled the ceiling is then the sole contender, its
	// share is the whole thing, and it retakes every slot it frees before the
	// starved tenant is ever counted. Measured on the holdings-only version under
	// sustained contention from two tenants: the greedy one won 2003 of 2003
	// admissions. Demand is what the arithmetic was missing.
	speechAsked = map[string]time.Time{}
)

// speechContenders counts the tenants competing for the ceiling at `at`: those
// holding a slot, those refused recently enough to still be trying, and org
// itself. Callers hold speechMu.
func speechContenders(org string, at time.Time) int {
	n := len(speechHeld)
	if _, holding := speechHeld[org]; !holding {
		n++ // this org is about to contend for a share of its own
	}
	for other, when := range speechAsked {
		if _, holding := speechHeld[other]; holding || other == org {
			continue // already counted
		}
		if at.Sub(when) < speechDemand {
			n++
		}
	}
	return n
}

// speechShare is how many slots one org may hold while contenders orgs are
// asking for the ceiling: an equal division of it, never below one. A share of
// zero would refuse a tenant holding nothing, which is not a small share but an
// outage — past that point the ceiling itself is the honest refusal.
func speechShare(contenders int) int {
	if contenders < 1 {
		contenders = 1
	}
	if s := speechCeiling / contenders; s >= 1 {
		return s
	}
	return 1
}

// speech refusal codes. They lead the message a caller is answered with and label
// the metric an operator scrapes, so both name the same condition by the same
// word. The two are separated because they ask for DIFFERENT responses: an org at
// its own share must finish or reduce its own work and gains nothing by retrying
// sooner, while a full service clears on its own.
const (
	speechOrgLimit = "speech_org_limit"
	speechCapacity = "speech_capacity"
)

var (
	// speechSlots is what each org holds right now — who has the capacity. A
	// child is dropped when its org falls to zero, so the series count follows
	// the ceiling rather than the number of tenants that have ever called.
	speechSlots = metric.NewGaugeVec(metric.GaugeOpts{
		Name: "cloud_speech_slots",
		Help: "Speech requests in flight, by org",
	}, []string{"org"})

	// speechRefusals is who is at a ceiling and WHICH ceiling — the question a
	// global counter cannot answer at all, and the one an operator asks when a
	// tenant reports that transcripts are falling behind. Two codes exist, so an
	// org contributes at most two series.
	speechRefusals = metric.NewCounterVec(metric.CounterOpts{
		Name: "cloud_speech_refused",
		Help: "Speech requests refused, by org and reason",
	}, []string{"org", "reason"})
)

// admitSpeech takes an in-flight slot for org, returning the release and, when
// there is no slot to give, the 429 the caller is answered with. It never blocks:
// a caller that cannot be served now is told so now.
//
// The org's own share is tested BEFORE the service ceiling, because when both are
// full the useful reason is the one the caller can act on. Telling an org that
// holds every running request "the service is busy" points it at other tenants
// for a condition it created by itself.
//
// A refusal takes no CAPACITY: no slot, no held entry, nothing to give back. It
// does record that the org asked, because a tenant being refused is otherwise
// invisible to the division — see speechAsked. That record expires, so a tenant
// that stops asking stops counting.
//
// The release is safe to call more than once. A second call would otherwise hand
// back a slot that was never taken — which does not fail, it quietly raises the
// ceiling for everyone one call at a time, with no signal that it happened.
func admitSpeech(org string) (release func(), refused error) {
	speechMu.Lock()
	defer speechMu.Unlock()

	now := time.Now()
	for other, when := range speechAsked {
		if now.Sub(when) >= speechDemand {
			delete(speechAsked, other) // a tenant that stopped asking stops counting
		}
	}

	held, total := speechHeld[org], 0
	for _, n := range speechHeld {
		total += n
	}

	if share := speechShare(speechContenders(org, now)); held >= share {
		speechAsked[org] = now
		return nil, speechRefuse(org, speechOrgLimit,
			"your org holds all %d of the speech requests it may run at once; "+
				"finish one before starting another", share)
	}
	if total >= speechCeiling {
		speechAsked[org] = now
		return nil, speechRefuse(org, speechCapacity,
			"all %d speech slots are in use; retry shortly", speechCeiling)
	}

	held++
	speechHeld[org] = held
	speechSlots.WithLabelValues(org).Set(float64(held))

	done := false
	return func() {
		speechMu.Lock()
		defer speechMu.Unlock()
		if done {
			return
		}
		done = true
		if n := speechHeld[org] - 1; n > 0 {
			speechHeld[org] = n
			speechSlots.WithLabelValues(org).Set(float64(n))
			return
		}
		delete(speechHeld, org)
		speechSlots.Delete(metric.Labels{"org": org})
	}, nil
}

// orgOf names the tenant in an "owner/name" identity — the shape zapResolveUser
// answers with on the legacy doors, which verify a principal but hand back only
// its address. The org is the verified owner, so this reads an attested value
// rather than deriving a new one.
func orgOf(who string) string {
	org, _, _ := strings.Cut(who, "/")
	return org
}

// speechRefuse counts a refusal and renders it in one step, so the reason an
// operator scrapes and the reason a caller reads cannot drift apart: there is no
// way to answer a caller without recording it, and none to record it without
// naming the same code back.
//
// The code leads the message because both transports carry a message and only one
// carries headers — putting the machine-readable part in the one channel they
// share is what keeps HTTP and ZAP answering the same thing.
func speechRefuse(org, code, detail string, a ...interface{}) error {
	speechRefusals.WithLabelValues(org, code).Inc()
	return busyError("%s: %s", code, fmt.Sprintf(detail, a...))
}
