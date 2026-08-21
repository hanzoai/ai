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
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// WHAT A RACE COSTS IS DECIDED WITH THE RESERVATION, never after. N attempts is
// N completions, and a hold sized for one is a promise that the ledger cannot
// keep — the losers' rows land later and take the balance past where the gate
// said it would stop.

// asking builds a request that asks for fast mode the way each kind of caller
// can. A browser cannot send a custom header past the edge's CORS allow-list, so
// the body is how a page asks; the header stays for callers with no browser in
// the way.
func asking(body, header string) *ApiController {
	c := visit("POST", "/v1/chat/completions")
	if body != "" {
		c.Fiber().Request().SetBody([]byte(body))
	}
	if header != "" {
		c.Fiber().Request().Header.Set("X-Fast", header)
	}
	return c
}

func TestFastIsOffUnlessItIsAskedFor(t *testing.T) {
	for _, body := range []string{``, `{"model":"m"}`, `{"fast":false}`} {
		if asking(body, "").wantsFast() {
			t.Errorf("body %q asked for nothing and got fast mode — every ordinary "+
				"request would silently cost twice as much", body)
		}
	}
}

func TestFastIsAskedByBodyOrHeader(t *testing.T) {
	for _, c := range []*ApiController{
		asking(`{"fast":true}`, ""),
		asking("", "1"),
		asking("", "true"),
		asking("", "TRUE"),
	} {
		if !c.wantsFast() {
			t.Error("a caller asked for fast mode and was not heard")
		}
	}
}

// The whole race is reserved before any of it is spent.
func TestAskingForFastReservesEveryAttempt(t *testing.T) {
	const subject, est = "fast-funded", int64(100)
	object.GlobalBalanceLedger.SetBalance(subject, 10_000)

	hold, wide, ok := asking(`{"fast":true}`, "").widthFor(&iam.User{}, subject, est)
	if !ok {
		t.Fatal("a funded account asked for fast mode and was refused")
	}
	defer hold.settle(0)
	if wide != fastWidth {
		t.Fatalf("width %d, want %d", wide, fastWidth)
	}
	avail, _ := object.GlobalBalanceLedger.Available(subject)
	if want := int64(10_000) - est*fastWidth; avail != want {
		t.Errorf("reserved down to %d, want %d: a hold sized for one completion while "+
			"%d are made lets the losers' rows take the balance past the gate",
			avail, want, fastWidth)
	}
}

// ASKING IS NOT GETTING, and being unable to afford it is not a refusal. The
// caller asked to go faster, not to be turned away — and inventing a second way
// for a funded account to be told no is worse than serving it normally.
func TestFastFallsBackRatherThanRefusing(t *testing.T) {
	const subject, est = "fast-thin", int64(100)
	// Enough for one completion, not for two.
	object.GlobalBalanceLedger.SetBalance(subject, 150)

	hold, wide, ok := asking(`{"fast":true}`, "").widthFor(&iam.User{}, subject, est)
	if !ok {
		t.Fatal("an account that can afford the ordinary request was refused outright " +
			"because it asked for fast mode — asking made it worse off")
	}
	defer hold.settle(0)
	if wide != 1 {
		t.Errorf("width %d, want 1: the race was not affordable and must not be run", wide)
	}
	avail, _ := object.GlobalBalanceLedger.Available(subject)
	if want := int64(150) - est; avail != want {
		t.Errorf("reserved down to %d, want %d — the fallback must hold one completion, "+
			"not the race it could not afford", avail, want)
	}
}

// A caller nobody can be billed for never races. The public lane makes no
// reservation, so hedging there spends N completions on a stranger and settles
// none of them.
func TestACallerNobodyCanBillNeverRaces(t *testing.T) {
	const subject, est = "fast-public", int64(100)
	object.GlobalBalanceLedger.SetBalance(subject, 10_000)

	hold, wide, ok := asking(`{"fast":true}`, "").widthFor(nil, subject, est)
	if !ok {
		t.Fatal("the public lane was refused")
	}
	defer hold.settle(0)
	if wide != 1 {
		t.Errorf("width %d for a caller with no account, want 1", wide)
	}
}

// An ordinary request reserves exactly what it always did. This is the row that
// says fast mode changed nothing for everybody else.
func TestAnOrdinaryRequestReservesOneCompletion(t *testing.T) {
	const subject, est = "fast-plain", int64(100)
	object.GlobalBalanceLedger.SetBalance(subject, 10_000)

	hold, wide, ok := asking(`{"model":"m"}`, "").widthFor(&iam.User{}, subject, est)
	if !ok {
		t.Fatal("an ordinary funded request was refused")
	}
	defer hold.settle(0)
	if wide != 1 {
		t.Fatalf("width %d, want 1", wide)
	}
	avail, _ := object.GlobalBalanceLedger.Available(subject)
	if want := int64(10_000) - est; avail != want {
		t.Errorf("reserved down to %d, want %d", avail, want)
	}
}
