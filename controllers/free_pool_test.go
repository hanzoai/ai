// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

// quotas is a vendor whose accounts run out of FREE requests the way OpenRouter's
// do: a 429 naming the window, with the reset in error.metadata.headers.
type quotas struct {
	mu     sync.Mutex
	spent  map[string]string // bearer key → "free-models-per-day" | "free-models-per-min" | "upstream"
	resets map[string]time.Time
	asked  []string
}

func (q *quotas) serve(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		_, _ = io.ReadAll(r.Body)
		q.mu.Lock()
		q.asked = append(q.asked, key)
		why, out := q.spent[key]
		reset := q.resets[key]
		q.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !out {
			_, _ = w.Write([]byte(`{"id":"gen-1","model":"v/big:free","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
			return
		}
		body := map[string]any{"error": map[string]any{"code": 429}}
		switch why {
		case "upstream":
			body["error"].(map[string]any)["message"] = "v/big:free is temporarily rate-limited upstream. Please retry shortly."
		default:
			e := body["error"].(map[string]any)
			e["message"] = "Rate limit exceeded: " + why + ". "
			if !reset.IsZero() {
				e["metadata"] = map[string]any{"headers": map[string]string{
					"X-RateLimit-Limit":     "50",
					"X-RateLimit-Remaining": "0",
					"X-RateLimit-Reset":     strconv.FormatInt(reset.UnixMilli(), 10),
				}}
			}
		}
		b, _ := json.Marshal(body)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func (q *quotas) calls() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.asked)
}

func (q *quotas) reset() {
	q.mu.Lock()
	q.asked = nil
	q.mu.Unlock()
}

func sendQuota(t *testing.T, url string, keys []string, free bool) (int, string) {
	t.Helper()
	resp, err := sendKeyed(keyedRequest(t, url), orProvider, keys, free, http.DefaultClient.Do)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// frozen stops the key clock at now for the rest of the test.
func frozen(t *testing.T, now time.Time) *time.Time {
	t.Helper()
	at := now
	keyNow = func() time.Time { return at }
	t.Cleanup(func() { keyNow = time.Now })
	return &at
}

// THE RING TURNS FOR FREE ROUTES. The vendor's free allowance is per account, so
// three accounts serve three accounts' worth of free requests only if requests are
// spread over them; a priced route keeps starting at the funded first key.
func TestFreeRequestsTurnTheRingAndPricedOnesDoNot(t *testing.T) {
	forgetKeys()
	a := &accounts{}
	s := a.serve(t)
	for range 3 {
		sendOnce(t, a, s.URL, threeKeys, true)
	}
	if got := a.calls(); !slices.Equal(got, threeKeys) {
		t.Fatalf("three free requests asked %v, want each account once in turn %v", got, threeKeys)
	}
	a.reset()
	for range 3 {
		sendOnce(t, a, s.URL, threeKeys, false)
	}
	if got := a.calls(); !slices.Equal(got, []string{"k1", "k1", "k1"}) {
		t.Fatalf("three priced requests asked %v, want the funded first key every time", got)
	}
}

// AN ACCOUNT OUT OF FREE REQUESTS FOR THE DAY is skipped by free routes until the
// vendor's reset, still serves priced ones, and is asked again once the reset
// passes.
func TestAKeyAtItsDailyFreeLimitSitsOutFreeRoutesUntilTheReset(t *testing.T) {
	forgetKeys()
	now := frozen(t, time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC))
	reset := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	q := &quotas{spent: map[string]string{"k1": "free-models-per-day"}, resets: map[string]time.Time{"k1": reset}}
	s := q.serve(t)

	if st, _ := sendQuota(t, s.URL, threeKeys, true); st != http.StatusOK {
		t.Fatalf("status %d, want k2 to serve", st)
	}
	if got := q.calls(); !slices.Equal(got, []string{"k1", "k2"}) {
		t.Fatalf("asked %v, want [k1 k2]", got)
	}

	q.reset()
	for range 3 {
		sendQuota(t, s.URL, threeKeys, true)
	}
	if slices.Contains(q.calls(), "k1") {
		t.Fatalf("free requests asked %v — k1 is out of free requests until %s", q.calls(), reset)
	}

	q.reset()
	delete(q.spent, "k1")
	sendQuota(t, s.URL, threeKeys, false)
	if got := q.calls(); !slices.Equal(got, []string{"k1"}) {
		t.Fatalf("a priced request asked %v, want k1 — a spent FREE quota says nothing about paid routes", got)
	}

	p := standing(threeKeys, *now)
	if p.State != PoolBusy || p.Ready != 2 || p.Keys != 3 || !p.Resets.Equal(reset) {
		t.Fatalf("pool = %+v, want busy, 2 of 3 ready, back at %s", p, reset)
	}

	*now = reset.Add(time.Second)
	q.reset()
	keyTurn.Store(0)
	sendQuota(t, s.URL, threeKeys, true)
	if got := q.calls(); !slices.Equal(got, []string{"k1"}) {
		t.Fatalf("after the reset asked %v, want k1 again", got)
	}
	if p := standing(threeKeys, *now); p.State != PoolAvailable || p.Ready != 3 {
		t.Fatalf("pool after the reset = %+v, want available", p)
	}
}

// A reset the vendor does not state is read as the window's own length.
func TestAnUnstatedResetIsTheWindowsLength(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		why  string
		want time.Time
	}{
		{"free-models-per-day", time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)},
		{"free-models-per-min", now.Add(time.Minute)},
	} {
		resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"Rate limit exceeded: ` + c.why + `","code":429}}`))}
		at, spent := freeQuota(resp, now)
		if !spent || !at.Equal(c.want) {
			t.Errorf("%s: reset %s (spent %v), want %s", c.why, at, spent, c.want)
		}
		if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), c.why) {
			t.Errorf("%s: the body was not put back: %q", c.why, b)
		}
	}
	resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(
		`{"error":{"message":"v/big:free is temporarily rate-limited upstream.","code":429}}`))}
	if _, spent := freeQuota(resp, now); spent {
		t.Error("an upstream provider's rate limit benched the account — it is not the account's quota")
	}
}

// A RATE LIMIT THAT IS NOT THE ACCOUNT'S — the model limited upstream — benches
// nothing, and on a free route it is answered after ONE account: every account
// would be told the same, so asking them all only multiplies the calls. The pool
// moves the request to another route instead. A priced route still asks the next
// account, which is the one that may be under its own rate.
func TestAnUpstreamRateLimitBenchesNoAccountAndAsksOne(t *testing.T) {
	forgetKeys()
	q := &quotas{spent: map[string]string{"k1": "upstream", "k2": "upstream", "k3": "upstream"}}
	s := q.serve(t)
	if st, _ := sendQuota(t, s.URL, threeKeys, true); st != http.StatusTooManyRequests {
		t.Fatalf("status %d, want the vendor's 429", st)
	}
	if got := q.calls(); len(got) != 1 {
		t.Fatalf("a free request asked %v, want one account — the model is limited, not the account", got)
	}
	if p := standing(threeKeys, keyNow()); p.State != PoolAvailable {
		t.Fatalf("pool = %+v, want available — nobody's quota is spent", p)
	}
	q.reset()
	sendQuota(t, s.URL, threeKeys, false)
	if got := q.calls(); !slices.Equal(got, threeKeys) {
		t.Fatalf("a priced request asked %v, want every account in order", got)
	}
}

// A per-minute answer that lands after a per-day one does not bring the key back early.
func TestALaterBenchIsNeverShortened(t *testing.T) {
	forgetKeys()
	now := frozen(t, time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC))
	midnight := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	coolUntil("k1", scopeFree, midnight)
	coolUntil("k1", scopeFree, now.Add(time.Minute))
	if at := back("k1", true, *now); !at.Equal(midnight) {
		t.Fatalf("k1 back at %s, want %s", at, midnight)
	}
}

// Keys the vendor REJECTS are not a spent quota: a free request with every key
// refused is a supply fault, restated as the 402 it always was, not the pool's 429.
func TestRejectedKeysAreNotASpentPool(t *testing.T) {
	forgetKeys()
	a := &accounts{status: map[string]int{"k1": 401, "k2": 401, "k3": 401}}
	s := a.serve(t)
	sendOnce(t, a, s.URL, threeKeys, true)
	a.reset()
	if st := sendOnce(t, a, s.URL, threeKeys, true); st != http.StatusPaymentRequired {
		t.Fatalf("status %d with every key rejected, want the supply 402", st)
	}
}

// Our own Free refusals, relayed by a service that fronts for us, are the
// caller's to read: a spent share is never served from the pool it bounds.
func TestOurFreeRefusalsAreNeverMovedToThePool(t *testing.T) {
	for _, code := range []string{object.CodeAllowanceSpent, object.CodePoolBusy, object.CodePoolExhausted} {
		if !billingNotice([]byte(`{"error":{"code":"` + code + `","message":"x"}}`)) {
			t.Errorf("%s is not recognised as our own refusal", code)
		}
	}
}

// A PER-MINUTE LIMIT ON EVERY ACCOUNT IS A BUSY POOL; A DAILY ONE IS AN EXHAUSTED
// POOL. And a free request that meets either is answered at once, without asking
// accounts the vendor already said are out.
func TestThePoolStateFollowsWhatTheAccountsSaid(t *testing.T) {
	forgetKeys()
	now := frozen(t, time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC))
	minute := now.Add(40 * time.Second)
	q := &quotas{
		spent:  map[string]string{"k1": "free-models-per-min", "k2": "free-models-per-min", "k3": "free-models-per-min"},
		resets: map[string]time.Time{"k1": minute, "k2": minute, "k3": minute},
	}
	s := q.serve(t)
	if st, _ := sendQuota(t, s.URL, threeKeys, true); st != http.StatusTooManyRequests {
		t.Fatalf("status %d, want the vendor's 429", st)
	}
	if p := standing(threeKeys, *now); p.State != PoolBusy || p.Ready != 0 || !p.Resets.Equal(minute) {
		t.Fatalf("pool = %+v, want busy until %s", p, minute)
	}

	forgetKeys()
	midnight := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	q.spent = map[string]string{"k1": "free-models-per-day", "k2": "free-models-per-day", "k3": "free-models-per-day"}
	q.resets = map[string]time.Time{"k1": midnight, "k2": midnight, "k3": midnight}
	sendQuota(t, s.URL, threeKeys, true)
	if p := standing(threeKeys, *now); p.State != PoolExhausted || p.Ready != 0 || !p.Resets.Equal(midnight) {
		t.Fatalf("pool = %+v, want exhausted until %s", p, midnight)
	}

	q.reset()
	st, body := sendQuota(t, s.URL, threeKeys, true)
	if st != http.StatusTooManyRequests || strings.Contains(body, "402") {
		t.Fatalf("status %d body %s, want a 429 — an exhausted free pool is not an account that cannot pay", st, body)
	}
	if got := q.calls(); len(got) != 0 {
		t.Fatalf("asked %v with every account out, want no round trip", got)
	}
	if st, _ := sendQuota(t, s.URL, threeKeys, false); st != http.StatusTooManyRequests {
		// Priced routes are still asked: the free quota says nothing about them.
		t.Fatalf("priced status %d, want the vendor asked (and its 429 relayed)", st)
	}
	if len(q.calls()) == 0 {
		t.Fatal("a priced request was answered from the free pool's state — paid routes must not share it")
	}
}

// An unconfigured pool has no account in it and says so.
func TestAPoolWithNoAccountIsExhausted(t *testing.T) {
	if p := standing(nil, time.Now()); p.State != PoolExhausted || p.Keys != 0 {
		t.Fatalf("pool = %+v, want exhausted with no keys", p)
	}
}

// THROUGH THE REAL PIPE: a free-lane request the pool cannot serve is answered
// with the Free plan's own refusal — what the pool is, when it refills, and the
// page that lifts it — never a vendor's words or a silent empty answer.
func TestAFreeRequestTheExhaustedPoolCannotServeIsToldSo(t *testing.T) {
	const free = "vendor/big:free"
	restore(t, engineFam)
	engineFam.urlKey = "TEST_ENGINE_URL_UNSET"
	engineFam.providerFn = nil
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "k2")
	t.Setenv("OPENROUTER_API_KEY_3", "k3")
	forgetKeys()
	cooled.forget()

	midnight := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	q := &quotas{
		spent:  map[string]string{"k1": "free-models-per-day", "k2": "free-models-per-day", "k3": "free-models-per-day"},
		resets: map[string]time.Time{"k1": midnight, "k2": midnight, "k3": midnight},
	}
	s := q.serve(t)
	spareFamily(t, s.URL, free)

	body := []byte(`{"model":"free","messages":[{"role":"user","content":"2+2?"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(freeFamily(), "chat/completions", "openai", "free", body, false, 0, "acme", nil, false, nil, time.Now()); out != nil {
		t.Fatalf("attempts=%+v — the pool's refusal is the answer, nothing is left to try", out)
	}
	if st := answered(c); st != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429; body %s", st, sent(c))
	}
	var got struct {
		Error struct {
			Message, Type, Code string
			ResetsAt            string `json:"resets_at"`
			UpgradeURL          string `json:"upgrade_url"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(sent(c)), &got); err != nil {
		t.Fatalf("body %s: %v", sent(c), err)
	}
	if got.Error.Code != object.CodePoolExhausted {
		t.Errorf("code %q, want %q", got.Error.Code, object.CodePoolExhausted)
	}
	for _, want := range []string{"Free plan", "pool shared by all free users", "used up", object.PayURL("", "acme")} {
		if !strings.Contains(got.Error.Message, want) {
			t.Errorf("message %q does not say %q", got.Error.Message, want)
		}
	}
	if got.Error.ResetsAt != midnight.Format(time.RFC3339) {
		t.Errorf("resets_at %q, want %q", got.Error.ResetsAt, midnight.Format(time.RFC3339))
	}
	if got.Error.UpgradeURL == "" {
		t.Error("no upgrade_url — the refusal must carry the way out")
	}
	if ra := string(c.Fiber().Response().Header.Peek("Retry-After")); ra == "" {
		t.Error("no Retry-After on a refusal that knows when it clears")
	}
	if sr := string(c.Fiber().Response().Header.Peek("x-should-retry")); sr != "false" {
		t.Errorf("x-should-retry = %q, want false — a pool spent until midnight is not retried in seconds", sr)
	}
}

// A FREE ROUTE THAT IS BUSY MOVES TO THE POOL. The free lane's own route (enso-auto
// on the enso service's one account) answering 429 while the pool's accounts sit
// idle would make a shared pool a queue for one key.
func TestABusyFreeRouteIsServedByThePool(t *testing.T) {
	const pooled = "vendor/big:free"
	restore(t, engineFam)
	engineFam.urlKey = "TEST_ENGINE_URL_UNSET"
	engineFam.providerFn = nil
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "k2")
	t.Setenv("OPENROUTER_API_KEY_3", "")
	forgetKeys()
	cooled.forget()

	q := &quotas{}
	s := q.serve(t)
	spareFamily(t, s.URL, pooled)

	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded: free-models-per-min.","code":429}}`))
	}))
	t.Cleanup(busy.Close)
	fam := otherFamily(t, busy.URL)
	fam.byID = map[string]zenModel{"enso-auto": {ID: "enso-auto"}} // discovered at zero: the free lane
	fam.ids = []string{"enso-auto"}

	body := []byte(`{"model":"enso-auto","messages":[{"role":"user","content":"2+2?"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(fam, "chat/completions", "openai", "enso-auto", body, false, 0, "acme", nil, false, nil, time.Now()); out != nil {
		t.Fatalf("attempts=%+v, want the pool to serve", out)
	}
	if st := answered(c); st != http.StatusOK || !strings.Contains(sent(c), `"ok"`) {
		t.Fatalf("status %d body %s, want the pool's answer", st, sent(c))
	}

	// A PRICED route that is busy stays busy: the caller paid for that model.
	priced := otherFamily(t, busy.URL)
	flash := zenModel{ID: "enso-flash"}
	flash.Base.In, flash.Base.Out = decimal.New(3, 0), decimal.New(15, 0)
	priced.byID = map[string]zenModel{"enso-flash": flash}
	if got := fallback(priced, "enso-flash", &apiError{status: http.StatusTooManyRequests, msg: "rate limited"}, nil); len(got) != 0 {
		t.Fatalf("routes=%v, want none — a busy priced route is waited out, not swapped", ids(got))
	}
}

// A FREE REQUEST THE POOL COULD NOT SERVE FOR A REASON OF ITS OWN is not the pool's
// to answer. Every account is ready and the routes simply failed (a 502), so the
// request keeps its path to the route's alternates rather than being told the pool
// is busy.
func TestAFailedWalkWithEveryAccountReadyIsNotThePools(t *testing.T) {
	restore(t, engineFam)
	engineFam.urlKey = "TEST_ENGINE_URL_UNSET"
	engineFam.providerFn = nil
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "k2")
	t.Setenv("OPENROUTER_API_KEY_3", "")
	forgetKeys()
	cooled.forget()

	a := &accounts{status: map[string]int{"k1": http.StatusBadGateway, "k2": http.StatusBadGateway}}
	s := a.serve(t)
	spareFamily(t, s.URL, "vendor/big:free")

	body := []byte(`{"model":"free","messages":[{"role":"user","content":"2+2?"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	out := c.pipeToFamily(freeFamily(), "chat/completions", "openai", "free", body, false, 0, "acme", nil, false, nil, time.Now())
	if len(out) == 0 {
		t.Fatalf("answered %d %s — a walk that failed with every account ready is handed back, not refused as the pool", answered(c), sent(c))
	}
	if strings.Contains(sent(c), object.CodePoolBusy) {
		t.Fatalf("told the caller the pool is busy: %s", sent(c))
	}
}

// A BUSY POOL NEVER PARKS A CLIENT FOR HOURS. One account spent for the day while
// the others still serve is "busy"; the refusal says a minute and carries no
// Retry-After out to the account's midnight.
func TestABusyRefusalCarriesNoLateReset(t *testing.T) {
	midnight := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	n := object.PoolRefused("api.hanzo.ai", "acme", PoolBusy, midnight, false)
	if n.Code != object.CodePoolBusy || !n.Resets.IsZero() || n.RetryAfter(time.Now()) != 0 {
		t.Fatalf("busy notice = %+v, want pool_busy with no reset", n)
	}
	soon := time.Now().Add(30 * time.Second)
	if n := object.PoolRefused("api.hanzo.ai", "acme", PoolBusy, soon, false); n.RetryAfter(time.Now()) == 0 {
		t.Fatal("a busy pool back within the minute should say when")
	}
	if n := object.PoolRefused("api.hanzo.ai", "acme", PoolExhausted, midnight, false); !n.Resets.Equal(midnight) {
		t.Fatalf("exhausted notice resets %s, want %s", n.Resets, midnight)
	}
}

// A PAYING CALLER ON A FREE ROUTE is not told to upgrade: they already have. They
// are told the free models' pool is busy or spent, and to pick a paid model.
func TestAPayingCallerIsNotAskedToUpgrade(t *testing.T) {
	n := object.PoolRefused("api.hanzo.ai", "acme", PoolExhausted, time.Now().Add(time.Hour), true)
	if n.Upgrade != "" || strings.Contains(n.Message, "Upgrade") || !strings.Contains(n.Message, "paid model") {
		t.Fatalf("paid notice = %+v, want no upgrade and a paid model named", n)
	}
	n = object.PoolRefused("api.hanzo.ai", "acme", PoolExhausted, time.Now().Add(time.Hour), false)
	if n.Upgrade == "" || !strings.Contains(n.Message, "Free plan") {
		t.Fatalf("free notice = %+v, want the Free plan named and the upgrade page", n)
	}
}
