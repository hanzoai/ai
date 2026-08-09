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

// What per-org admission has to be true for, stated as the failures it prevents:
// one tenant cannot take the capacity of another, a refused caller is told which
// ceiling it hit, a refusal costs nothing, and an operator can see who is at a
// ceiling without asking anyone.

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// speechIdle starts a test from an empty ceiling and leaves one behind. The state
// is process-global by design — it IS this process's capacity — so a test that
// left something behind would shrink the ceiling for every test after it and the
// failure would surface somewhere unrelated.
//
// Both maps, not just holdings: a refusal is remembered for speechDemand, so one
// test's refused org keeps dividing the ceiling inside the next one for five
// seconds. That is correct in production and poison between tests.
func speechIdle(t *testing.T) {
	t.Helper()
	drain := func() {
		speechMu.Lock()
		defer speechMu.Unlock()
		speechHeld = map[string]int{}
		speechAsked = map[string]time.Time{}
		speechSlots.Reset()
	}
	drain()
	t.Cleanup(drain)
}

// take admits one request for org, failing the test if it was refused.
func take(t *testing.T, org string) func() {
	t.Helper()
	release, refused := admitSpeech(org)
	if refused != nil {
		t.Fatalf("%s was refused while it should have been admitted: %v", org, refused)
	}
	return release
}

// refuse asserts org is refused, and returns the code the refusal leads with.
func refuse(t *testing.T, org string) (code, msg string) {
	t.Helper()
	release, refused := admitSpeech(org)
	if refused == nil {
		release()
		t.Fatalf("%s was ADMITTED where it should have been refused", org)
	}
	msg = refused.Error()
	if statusOf(refused) != http.StatusTooManyRequests {
		t.Fatalf("refusal carried status %d, want 429: %q", statusOf(refused), msg)
	}
	code, _, _ = strings.Cut(msg, ":")
	return code, msg
}

// TestSpeechShareDividesTheCeiling states the arithmetic on its own: an equal
// split of the ceiling, floored at one. The floor is the interesting half — a
// share of zero would refuse a tenant that holds nothing, which is not a small
// share but an outage, and past that point the ceiling itself is the honest
// refusal rather than a division that has stopped meaning anything.
func TestSpeechShareDividesTheCeiling(t *testing.T) {
	for _, tc := range []struct{ contenders, want int }{
		{0, speechCeiling}, // no one asking yet: the next caller may have it all
		{1, speechCeiling},
		{2, speechCeiling / 2},
		{speechCeiling, 1},
		{speechCeiling + 1, 1},
		{100, 1},
	} {
		if got := speechShare(tc.contenders); got != tc.want {
			t.Errorf("share among %d orgs = %d, want %d", tc.contenders, got, tc.want)
		}
	}
}

// TestALoneOrgHoldsTheWholeCeiling proves nothing is reserved away from a tenant
// nobody is competing with: idle capacity is usable capacity. It also pins WHICH
// refusal a lone org gets when it fills the service by itself — its own limit,
// not "the service is busy", because pointing an org that owns every running
// request at other tenants would be a lie it cannot act on.
func TestALoneOrgHoldsTheWholeCeiling(t *testing.T) {
	speechIdle(t)

	for i := 0; i < speechCeiling; i++ {
		defer take(t, "acme")()
	}
	code, msg := refuse(t, "acme")
	if code != speechOrgLimit {
		t.Fatalf("a lone org that filled the ceiling was refused %q, want %q: %s", code, speechOrgLimit, msg)
	}
}

// TestOneOrgCannotStarveAnother is the whole point of this file.
//
// Under a single global ceiling, an org that filled it kept it: every slot it
// freed was one it could immediately retake, and every other tenant was refused
// for as long as the first kept working. That is the failure — one org's
// all-hands starving every other tenant, with no signal to anyone.
//
// Here the freed slot goes to the STARVED tenant instead, because the share is
// recomputed against who is contending: the moment a second org is asking, the
// first is over its half and is refused its next request. The split converges to
// even without interrupting a decode that is already running.
func TestOneOrgCannotStarveAnother(t *testing.T) {
	speechIdle(t)

	// Loud fills the idle ceiling — legitimately, since nobody else was asking.
	loud := make([]func(), 0, speechCeiling)
	for i := 0; i < speechCeiling; i++ {
		loud = append(loud, take(t, "loud"))
	}

	// Quiet arrives to a full service. There is no slot to give it, and the
	// refusal says exactly that rather than blaming quiet for its own usage.
	if code, msg := refuse(t, "quiet"); code != speechCapacity {
		t.Fatalf("quiet was refused %q on a full service, want %q: %s", code, speechCapacity, msg)
	}

	// Loud finishes one request. THIS is the moment that used to go wrong.
	loud[0]()
	loud = loud[1:]

	quiet := []func(){take(t, "quiet")}

	// ... and loud cannot take it back: two orgs are contending, so loud is over
	// its half and waits its turn.
	if code, msg := refuse(t, "loud"); code != speechOrgLimit {
		t.Fatalf("loud reclaimed the slot it freed (refused %q, want %q): %s", code, speechOrgLimit, msg)
	}

	// As loud drains, quiet keeps taking, until the ceiling is split evenly.
	loud[0]()
	loud = loud[1:]
	quiet = append(quiet, take(t, "quiet"))

	speechMu.Lock()
	held := map[string]int{"loud": speechHeld["loud"], "quiet": speechHeld["quiet"]}
	speechMu.Unlock()
	want := speechCeiling / 2
	if held["loud"] != want || held["quiet"] != want {
		t.Fatalf("capacity settled at loud=%d quiet=%d, want %d each", held["loud"], held["quiet"], want)
	}

	for _, r := range append(loud, quiet...) {
		r()
	}
}

// TestDemandExpires guards the cost of remembering demand at all.
//
// Counting refused tenants is what stops a greedy one from retaking every slot it
// frees, but it buys that with memory, and memory that never lets go is a worse
// failure than the one it fixed: an org that asked once and vanished would divide
// the ceiling forever, so the service would quietly shrink toward a quarter of
// itself with nobody using it and no error anywhere.
//
// Asserted on the arithmetic directly, with times passed in, so it needs no sleep
// and cannot be flaky. The second half is the map: expiry that only affects the
// COUNT still leaks an entry per org, forever.
func TestDemandExpires(t *testing.T) {
	speechIdle(t)
	now := time.Now()

	speechMu.Lock()
	speechHeld["here"] = 1
	speechAsked["gone"] = now.Add(-speechDemand)
	speechAsked["trying"] = now.Add(-speechDemand / 2)
	got := speechContenders("here", now)
	speechMu.Unlock()

	if got != 2 {
		t.Fatalf("contenders = %d, want 2 (the holder and the tenant still asking) — "+
			"a tenant that stopped asking is still dividing the ceiling", got)
	}

	release, refused := admitSpeech("here")
	if refused == nil {
		defer release()
	}
	speechMu.Lock()
	_, stale := speechAsked["gone"]
	_, live := speechAsked["trying"]
	speechMu.Unlock()
	if stale {
		t.Error("an expired demand entry is still in the map; it grows by one per org, forever")
	}
	if !live {
		t.Error("a tenant that is still asking was forgotten; it will be starved again")
	}
}

// TestGreedDoesNotPay measures the outcome the design exists for, rather than
// asserting a branch: under sustained contention, what SHARE of the capacity does
// a greedy tenant actually get?
//
// Method — deterministic, no goroutines and no clock, so the number is the
// policy's and not the scheduler's. Each round: `greedy` asks until refused (it
// always wants more), `quiet` asks once, then the oldest running request
// finishes. Admissions are counted per org over 2000 rounds.
//
// A single global ceiling gives greedy essentially all of it: it is the one
// asking when a slot frees. Dividing by share converges to an even split, which
// is the whole claim, in a number.
func TestGreedDoesNotPay(t *testing.T) {
	speechIdle(t)

	var running []func()
	won := map[string]int{}
	ask := func(org string) bool {
		release, refused := admitSpeech(org)
		if refused != nil {
			return false
		}
		won[org]++
		running = append(running, release)
		return true
	}

	const rounds = 2000
	for i := 0; i < rounds; i++ {
		for ask("greedy") {
		}
		ask("quiet")
		if len(running) > 0 {
			running[0]()
			running = running[1:]
		}
	}
	for _, r := range running {
		r()
	}

	total := won["greedy"] + won["quiet"]
	if total == 0 {
		t.Fatal("nothing was admitted in 2000 rounds; the measurement is broken, not the code")
	}
	got := float64(won["quiet"]) / float64(total)
	// Half, less the head start greedy keeps from filling an idle ceiling first.
	if got < 0.45 {
		t.Fatalf("the quiet tenant won %.1f%% of %d admissions (greedy %d, quiet %d); "+
			"a tenant that simply asks more often is taking the service",
			got*100, total, won["greedy"], won["quiet"])
	}
	t.Logf("quiet won %.1f%% of %d admissions (greedy %d, quiet %d)",
		got*100, total, won["greedy"], won["quiet"])
}

// TestRefusalNamesWhichCeiling proves the two refusals are distinguishable and
// that each names something the caller can act on. They are not decoration: an
// org at its own share gains nothing by retrying sooner and must finish or reduce
// its own work, while a full service clears without the caller doing anything.
// A client that cannot tell them apart will retry the one case where retrying is
// exactly wrong.
func TestRefusalNamesWhichCeiling(t *testing.T) {
	speechIdle(t)

	// One org alone, over its (whole-ceiling) share.
	held := make([]func(), 0, speechCeiling)
	for i := 0; i < speechCeiling; i++ {
		held = append(held, take(t, "acme"))
	}
	mine, mineMsg := refuse(t, "acme")

	// A second org, against a ceiling with nothing free.
	theirs, theirsMsg := refuse(t, "globex")

	for _, r := range held {
		r()
	}

	if mine == theirs {
		t.Fatalf("both refusals report %q; a caller cannot tell its own limit from a busy service", mine)
	}
	if mine != speechOrgLimit || theirs != speechCapacity {
		t.Fatalf("codes are (own=%q, service=%q), want (%q, %q)", mine, theirs, speechOrgLimit, speechCapacity)
	}
	// Each names its number, so a human reading the message can act without
	// reading this source.
	if !strings.Contains(mineMsg, "your org") {
		t.Errorf("own-limit refusal does not say it is the caller's own: %q", mineMsg)
	}
	for _, msg := range []string{mineMsg, theirsMsg} {
		if !strings.Contains(msg, "4") {
			t.Errorf("refusal names no number, so nobody can size against it: %q", msg)
		}
	}
}

// TestRefusalTakesNothing proves a refused request costs nothing to answer: it
// holds no slot and, crucially, creates no entry for the org.
//
// An entry would make a refused org COUNT as a contender, so an org that never
// gets served would still shrink everyone else's share — a tenant able to reduce
// the capacity of every other tenant purely by being refused.
func TestRefusalTakesNothing(t *testing.T) {
	speechIdle(t)

	held := make([]func(), 0, speechCeiling)
	for i := 0; i < speechCeiling; i++ {
		held = append(held, take(t, "acme"))
	}
	for i := 0; i < 50; i++ {
		refuse(t, "flood")
	}

	speechMu.Lock()
	_, present := speechHeld["flood"]
	orgs, acme := len(speechHeld), speechHeld["acme"]
	speechMu.Unlock()

	if present {
		t.Error("a refused org holds an entry — being refused would shrink every other org's share")
	}
	if orgs != 1 || acme != speechCeiling {
		t.Errorf("after 50 refusals the state is %d orgs / acme=%d, want 1 / %d", orgs, acme, speechCeiling)
	}

	for _, r := range held {
		r()
	}
	speechMu.Lock()
	left := len(speechHeld)
	speechMu.Unlock()
	if left != 0 {
		t.Errorf("%d orgs still hold slots after every release — the ceiling leaks", left)
	}
}

// TestReleaseIsIdempotent proves a slot handed back twice is only handed back
// once. A double release does not fail loudly; it quietly hands the org back
// capacity it is still using, and nothing reports that it happened — the shape of
// failure this estate hits most often.
//
// The org must hold MORE than the slot being released for this to bite, which is
// the whole subtlety: with a single slot held, a second release underflows to a
// count that the delete-at-zero path absorbs, and the leak hides. A test that only
// ever releases the last slot passes against a release with no guard at all.
func TestReleaseIsIdempotent(t *testing.T) {
	speechIdle(t)

	first := take(t, "acme")
	second := take(t, "acme")
	defer second()

	first()
	first() // the same slot, handed back again

	speechMu.Lock()
	held := speechHeld["acme"]
	speechMu.Unlock()
	if held != 1 {
		t.Fatalf("acme holds %d after releasing ONE of its two slots twice, want 1 — "+
			"the second release gave back a slot that is still running", held)
	}
}

// TestAdmissionNeverBlocks proves a refused caller is refused NOW. A ceiling that
// queues converts a burst into latency for every caller and holds each waiting
// body's memory while it does — the failure the ceiling exists to prevent, and
// the reason this refuses instead of waiting for a slot.
func TestAdmissionNeverBlocks(t *testing.T) {
	speechIdle(t)

	held := make([]func(), 0, speechCeiling)
	for i := 0; i < speechCeiling; i++ {
		held = append(held, take(t, "acme"))
	}
	defer func() {
		for _, r := range held {
			r()
		}
	}()

	done := make(chan bool, 1)
	go func() {
		release, refused := admitSpeech("globex")
		if refused == nil {
			release()
		}
		done <- refused == nil
	}()
	select {
	case admitted := <-done:
		if admitted {
			t.Fatal("admitted past the ceiling")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admitSpeech BLOCKED when full; it must refuse immediately")
	}
}

// TestCeilingHoldsUnderConcurrency proves the bound holds under real concurrent
// traffic from many tenants and that every slot comes back. A leak would silently
// reduce capacity toward zero and take the endpoint down with no attacker
// involved and no error to point at.
func TestCeilingHoldsUnderConcurrency(t *testing.T) {
	speechIdle(t)

	var wg sync.WaitGroup
	var mu sync.Mutex
	inFlight, peak := 0, 0

	orgs := []string{"acme", "globex", "initech", "umbrella", "soylent"}
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release, refused := admitSpeech(orgs[i%len(orgs)])
			if refused != nil {
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()

			mu.Lock()
			inFlight--
			mu.Unlock()
			release()
		}(i)
	}
	wg.Wait()

	if peak > speechCeiling {
		t.Fatalf("peak in-flight was %d, over the ceiling of %d", peak, speechCeiling)
	}
	speechMu.Lock()
	left := len(speechHeld)
	speechMu.Unlock()
	if left != 0 {
		t.Fatalf("%d orgs still hold slots after every goroutine finished — the ceiling leaks", left)
	}
}

// TestNamingAnotherOrgSpendsYourOwn is the containment proof: the ceiling is a
// property of the credential, not of the wire.
//
// A client stamping X-Org-Id with a tenant it does not belong to is answered from
// its OWN share — billingOrg refuses the switch rather than redirecting it — so
// the named org's capacity is untouched and cannot be exhausted by a stranger.
// Keying admission on the raw header, or on anything else a caller writes, is
// what this forbids.
func TestNamingAnotherOrgSpendsYourOwn(t *testing.T) {
	speechIdle(t)

	// An sk- IAM key owned by "hanzo", asking to act as "victim". Only a signed
	// membership claim can move the wallet, and an API key carries none.
	user := &iam.User{Owner: "hanzo", Name: "alice"}
	c := orgController("Bearer sk-abc", "victim")

	held := make([]func(), 0, speechCeiling)
	for i := 0; i < speechCeiling; i++ {
		held = append(held, take(t, c.billingOrg(user)))
	}
	defer func() {
		for _, r := range held {
			r()
		}
	}()

	speechMu.Lock()
	spent := map[string]int{}
	for org, n := range speechHeld {
		spent[org] = n
	}
	speechMu.Unlock()

	if n := spent["victim"]; n != 0 {
		t.Fatalf("the header moved %d slots onto victim's share — a caller can exhaust a tenant it has no claim to", n)
	}
	if n := spent[user.Owner]; n != speechCeiling {
		t.Fatalf("%s holds %d of the %d slots it spent, want all of them (state: %v)",
			user.Owner, n, speechCeiling, spent)
	}
}

// TestWhoIsAtTheirCeilingIsVisible proves an operator can answer "who is at their
// ceiling, and why" from the metrics endpoint alone, without asking a tenant and
// without reading a log.
//
// It scrapes the SAME handler GET /v1/metrics serves, so this fails if the
// counters are recorded into a registry nobody exposes — the way a metric most
// often turns out not to exist at the moment it is needed.
func TestWhoIsAtTheirCeilingIsVisible(t *testing.T) {
	speechIdle(t)

	held := make([]func(), 0, speechCeiling)
	for i := 0; i < speechCeiling; i++ {
		held = append(held, take(t, "loudorg"))
	}
	refuse(t, "loudorg")  // at its OWN limit
	refuse(t, "quietorg") // shut out by a full service

	rr := httptest.NewRecorder()
	object.MetricsHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/metrics", nil))
	scrape := rr.Body.String()

	for _, want := range []string{
		`cloud_speech_slots{org="loudorg"} 4`,
		`cloud_speech_refused{org="loudorg",reason="speech_org_limit"} 1`,
		`cloud_speech_refused{org="quietorg",reason="speech_capacity"} 1`,
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape does not carry %s\n--- scrape ---\n%s", want, speechLines(scrape))
		}
	}

	// A tenant that finishes stops being reported, so the series count follows
	// the ceiling rather than growing with every org that has ever called.
	for _, r := range held {
		r()
	}
	rr = httptest.NewRecorder()
	object.MetricsHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/metrics", nil))
	if strings.Contains(rr.Body.String(), `cloud_speech_slots{org="loudorg"}`) {
		t.Errorf("an idle org still reports a slots series:\n%s", speechLines(rr.Body.String()))
	}
}

// speechLines keeps a failure message to the speech metrics.
func speechLines(scrape string) string {
	var out []string
	for _, line := range strings.Split(scrape, "\n") {
		if strings.HasPrefix(line, "cloud_speech") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// speechSource is the package's own source, read as data. Two invariants below
// are properties of where code is WRITTEN rather than of what it computes, and
// asserting them anywhere else would be asserting something weaker.
func speechSource(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(f os.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse controllers: %v", err)
	}
	files := map[string]*ast.File{}
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			files[filepath.Base(path)] = file
		}
	}
	return fset, files
}

// billsAt returns where fn builds a usage record carrying audio quantity, or -1.
// That — not a function name — is what makes something a speech meter, so a new
// transport is bound by the rules below the moment it charges for audio, without
// anyone remembering to add it to a list.
func billsAt(fn ast.Node) int {
	at := -1
	ast.Inspect(fn, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if k, ok := kv.Key.(*ast.Ident); ok && (k.Name == "AudioSeconds" || k.Name == "AudioChars") {
			if at < 0 || int(kv.Pos()) < at {
				at = int(kv.Pos())
			}
		}
		return true
	})
	return at
}

// earlier folds positions, ignoring the absent ones.
func earlier(positions ...int) int {
	at := -1
	for _, p := range positions {
		if p >= 0 && (at < 0 || p < at) {
			at = p
		}
	}
	return at
}

// calledAt returns the position of the first call to name inside n, or -1.
func calledAt(n ast.Node, name string) int {
	at := -1
	ast.Inspect(n, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == name && at < 0 {
				at = int(call.Pos())
			}
		case *ast.SelectorExpr:
			if f.Sel.Name == name && at < 0 {
				at = int(call.Pos())
			}
		}
		return true
	})
	return at
}

// speechWork names the two calls that spend speech capacity: the provider
// interfaces that transcribe and synthesize. Every door to the models — the
// OpenAI-shaped pair, the legacy store-bound pair, and both of their ZAP twins —
// ends in one of these, whatever else it does first.
var speechWork = []string{"ProcessAudio", "QueryAudio"}

// TestSpeechWorkRunsUnderAdmission is the completeness rule: a limit that one URL
// gets around is not a limit.
//
// It finds every function that spends speech capacity and requires admission
// before it. Nothing else can state this — a limit is invisible from inside the
// handler that lacks it, and four doors to these models (the legacy store-bound
// STT and TTS routes and their ZAP twins) were in exactly that position: bounded
// siblings next to them, and no bound at all on themselves.
//
// Written as a discovery rather than a list, so a door added later is bound by
// this the moment it calls a provider, not when someone remembers it exists.
func TestSpeechWorkRunsUnderAdmission(t *testing.T) {
	_, files := speechSource(t)

	doors := 0
	for name, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			work := -1
			for _, w := range speechWork {
				if at := calledAt(fn, w); at >= 0 && (work < 0 || at < work) {
					work = at
				}
			}
			if work < 0 {
				continue
			}
			doors++
			admit := calledAt(fn, "admitSpeech")
			if admit < 0 {
				t.Errorf("%s.%s spends speech capacity and takes no admission — "+
					"one tenant can have all of it through this door", name, fn.Name.Name)
				continue
			}
			if admit > work {
				t.Errorf("%s.%s starts the work before admission; the ceiling is decorative there",
					name, fn.Name.Name)
			}
		}
	}
	// Self-test the instrument against a known positive: eight doors reach these
	// models today. A discovery that quietly finds nothing reads exactly like a
	// rule that passed.
	if doors < 8 {
		t.Fatalf("found only %d speech doors; the walk is broken, not the code", doors)
	}
}

// TestChargingForSpeechRequiresAdmission proves a refusal cannot reach the meter.
//
// The property is an ORDER, and nothing observable at runtime distinguishes
// "refused, so never metered" from "refused after being metered" — the record
// would simply exist — so the guard has to read the source.
//
// It is stated at the granularity the code actually has. A batch call holds its
// slot inside one function, so there the order is within that function. A
// streaming session holds ONE slot across many pushes, so its meter runs under a
// slot taken by a different function of the same route group; demanding they be
// the same function would forbid a design that is correct. What covers both, and
// what matters, is that no route group charges for audio without taking
// admission, and that no function doing both bills first.
func TestChargingForSpeechRequiresAdmission(t *testing.T) {
	_, files := speechSource(t)

	// The meters, discovered by what they BUILD. Charging is then either building
	// such a record or calling something that does — so a handler that hands the
	// job to recordAudioUsage is charging just as much as the function that fills
	// the struct, and both are held to the same order.
	meters := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && billsAt(fn) >= 0 {
				meters[fn.Name.Name] = true
			}
		}
	}
	if len(meters) < 2 {
		t.Fatalf("found only %d audio meters %v; the walk is broken, not the code", len(meters), meters)
	}

	charging := 0
	for name, file := range files {
		fileAdmits := calledAt(file, "admitSpeech") >= 0
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			charge := billsAt(fn)
			for meter := range meters {
				if meter != fn.Name.Name {
					charge = earlier(charge, calledAt(fn, meter))
				}
			}
			if charge < 0 {
				continue
			}
			charging++
			if !fileAdmits {
				t.Errorf("%s.%s charges for audio and nothing in %s takes admission — "+
					"this route is unbounded, and one tenant can have all of it", name, fn.Name.Name, name)
				continue
			}
			if admit := calledAt(fn, "admitSpeech"); admit >= 0 && charge < admit {
				t.Errorf("%s.%s charges before admission; a refused request would be billed", name, fn.Name.Name)
			}
		}
	}
	// Self-test the instrument against a known positive.
	if charging < 6 {
		t.Fatalf("found only %d functions that charge for audio; the walk is broken, not the code", charging)
	}
}

// TestAdmissionKeyComesFromTheCredential is the containment rule written where it
// can be enforced: a share may only ever be keyed on an org derived from the
// verified principal.
//
// Every admissible key is listed here WITH the reason it is admissible, so adding
// a third makes someone say why it cannot be spoofed instead of discovering later
// that it can. Key the ceiling on c.Ctx.Input.Header("X-Org-Id"), or on any other
// value a caller writes, and this fails on the spot.
//
// TestNamingAnotherOrgSpendsYourOwn is the other half: this says the key is one of
// these expressions, that says these expressions cannot be moved by a header.
func TestAdmissionKeyComesFromTheCredential(t *testing.T) {
	// Both are resolved AFTER identity resolution, from the credential:
	//   c.billingOrg(authUser) — the ledger the request spends from. A switch is
	//                            honored only for a signed `orgs` membership claim.
	//   authUser.Owner         — the principal's home org, on a transport that has
	//                            no headers at all to carry a switch.
	//   c.GetOrg()             — the org the request ACTS IN, resolved from the
	//                            verified principal; a header naming an org the
	//                            principal does not belong to answers with theirs.
	//   orgOf(who)             — the owner half of the identity zapResolveUser
	//                            returns, which it produces only from a validated
	//                            API key or a signature-checked JWT.
	attested := map[string]bool{
		"c.billingOrg(authUser)": true,
		"authUser.Owner":         true,
		"c.GetOrg()":             true,
		"orgOf(who)":             true,
	}

	fset, files := speechSource(t)
	keys := 0
	for name, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "admitSpeech" || len(call.Args) != 1 {
				return true
			}
			keys++
			var b strings.Builder
			if err := printer.Fprint(&b, fset, call.Args[0]); err != nil {
				t.Fatalf("render: %v", err)
			}
			if !attested[b.String()] {
				t.Errorf("%s keys the speech ceiling on %q, which is not a listed attested org — "+
					"a caller may be able to spend a tenant it does not belong to", name, b.String())
			}
			return true
		})
	}
	if keys == 0 {
		t.Fatal("no admitSpeech call was found anywhere; the walk is broken, not the code")
	}
}
