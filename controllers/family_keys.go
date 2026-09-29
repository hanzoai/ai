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

// family_keys.go — a family served on several vendor accounts spends them as one pool.
//
// Every family request leaves through dispatch, and dispatch sends through
// modelFamily.send. A family that names more than one credential (keyNames) gets
// the ring below; every other family sends exactly as it did.
//
// A PRICED route tries the accounts in order, because the first is the one kept
// funded. A FREE route starts one account further round the ring on every request,
// because the vendor's free allowance is per account: turning the ring is what
// makes three accounts serve three accounts' worth of free requests, where
// starting at the first would spend one and leave two idle.
//
// An account-level refusal moves the SAME request to the next account:
//
//	402  the account cannot pay          → next key; this key cools for priced routes
//	401  the key is not accepted         → next key; this key cools for every route
//	429  the account's free quota is out → next key; this key cools for free routes
//	     until the vendor says it resets
//	429  any other, on a priced route    → next key; nothing cools
//	429  any other, on a free route      → answered as it came: the MODEL is limited
//	     upstream, every account would hear the same, so the request moves to
//	     another route rather than another key
//
// Anything else is the request's own answer and is returned as it came. When no
// key answers, the last refusal is returned unchanged, so the pipe maps it the
// way it maps any vendor refusal (a supply refusal, never the caller's debt).
//
// What the keys have said about free routes is also the free pool's STANDING
// (Pool): how many accounts can take a free request now, and when the first one
// that cannot comes back. The Free plan's usage page reads it, so a person who
// shares the pool can see why it refused them.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
)

// keyCool is how long a key that answered 402 or 401 sits out before it is
// asked again.
const keyCool = 3 * time.Minute

// keyScope is what a cooled key is out for. A 402 says the account cannot pay,
// which says nothing about a route the vendor serves for free; a 401 says the
// key itself is refused, which holds for every route; a spent free quota says
// nothing about a route the account pays for.
type keyScope int

const (
	scopePriced keyScope = iota
	scopeAll
	scopeFree
)

type keyCooling struct {
	id    string // keyID, never the key
	scope keyScope
}

var (
	keyCoolMu sync.Mutex
	keyCooled = map[keyCooling]time.Time{}
	keyNow    = time.Now // the clock the cooldown reads; tests move it

	// keyTurn is where the next free request starts round the ring.
	keyTurn atomic.Uint64
)

// keyID names a key in memory and in logs without holding the key.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// scopes are the coolings that bench a key for a route of this price.
func scopes(free bool) []keyScope {
	if free {
		return []keyScope{scopeAll, scopeFree}
	}
	return []keyScope{scopeAll, scopePriced}
}

// back is when key may next serve a route of this price: zero when it may now.
func back(key string, free bool, now time.Time) time.Time {
	id := keyID(key)
	keyCoolMu.Lock()
	defer keyCoolMu.Unlock()
	var out time.Time
	for _, s := range scopes(free) {
		k := keyCooling{id, s}
		until, ok := keyCooled[k]
		if !ok {
			continue
		}
		if !now.Before(until) {
			delete(keyCooled, k) // expired: probe it again
			continue
		}
		if until.After(out) {
			out = until
		}
	}
	return out
}

// cooling reports whether key sits out a request for a route of this price.
func cooling(key string, free bool) bool {
	return !back(key, free, keyNow()).IsZero()
}

func cool(key string, scope keyScope) { coolUntil(key, scope, keyNow().Add(keyCool)) }

// coolUntil benches key for scope until the later of what it already sits out
// and until: a per-minute answer that arrives after a per-day one must not bring
// the key back early.
func coolUntil(key string, scope keyScope, until time.Time) {
	keyCoolMu.Lock()
	k := keyCooling{keyID(key), scope}
	if until.After(keyCooled[k]) {
		keyCooled[k] = until
	}
	keyCoolMu.Unlock()
}

// quotaBenched reports whether any of keys sits out free routes for its free
// quota — the one bench that makes a refused free request the pool's to state.
func quotaBenched(keys []string, now time.Time) bool {
	keyCoolMu.Lock()
	defer keyCoolMu.Unlock()
	for _, k := range keys {
		if until, ok := keyCooled[keyCooling{keyID(k), scopeFree}]; ok && now.Before(until) {
			return true
		}
	}
	return false
}

// forgetKeys clears every cooldown and turns the ring back to its first key.
// Tests start from it.
func forgetKeys() {
	keyCoolMu.Lock()
	keyCooled = map[keyCooling]time.Time{}
	keyCoolMu.Unlock()
	keyTurn.Store(0)
}

// keys returns the credentials this family's requests try, in order, or nil
// when it sends on the provider's one key.
func (f *modelFamily) keys() []string {
	if len(f.keyNames) == 0 {
		return nil
	}
	return object.FamilyKeys(f.name, f.keyNames)
}

// ring is every credential a family with a ring spends: its named keys, or the
// one key its admin row supplies. A family without a ring has none. It is what
// the pool's standing counts; sending uses keys, so an admin row's one key is
// sent as it always was and is never benched for every tenant by one request.
func (f *modelFamily) ring(p *object.Provider) []string {
	if len(f.keyNames) == 0 {
		return nil
	}
	if keys := f.keys(); len(keys) > 0 {
		return keys
	}
	if p != nil && strings.TrimSpace(p.ClientSecret) != "" {
		return []string{p.ClientSecret}
	}
	return nil
}

// send issues r to the family's upstream. free says the route is one the vendor
// charges nothing for. A streamed request is judged by its opening frames on each
// key (family_open.go), so a refusal that arrives inside a 200 stream moves to the
// next key exactly as one in the status does.
func (f *modelFamily) send(r *http.Request, p *object.Provider, free, stream bool) (*http.Response, error) {
	do := zenPipeClient.Do
	if stream {
		do = func(r *http.Request) (*http.Response, error) {
			resp, err := zenPipeClient.Do(r)
			if err != nil {
				return nil, err
			}
			return opening(resp), nil
		}
	}
	keys := f.keys()
	if len(keys) == 0 {
		return do(r)
	}
	return sendKeyed(r, p, keys, free, do)
}

// order is the sequence one request tries the keys in: from the first for a
// priced route, from the next turn of the ring for a free one.
func order(keys []string, free bool) []string {
	if !free || len(keys) < 2 {
		return keys
	}
	start := int((keyTurn.Add(1) - 1) % uint64(len(keys)))
	return append(append([]string(nil), keys[start:]...), keys[:start]...)
}

// sendKeyed issues r on each key in turn until an upstream gives an answer
// that is not an account refusal. r must carry GetBody so each attempt sends
// the same body.
func sendKeyed(r *http.Request, p *object.Provider, keys []string, free bool, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	var last *http.Response
	for _, k := range order(keys, free) {
		if cooling(k, free) {
			continue
		}
		rr := r.Clone(r.Context())
		if r.GetBody != nil {
			b, err := r.GetBody()
			if err != nil {
				return nil, err
			}
			rr.Body = b
		}
		pk := *p
		pk.ClientSecret = k
		upstream.Authorize(rr, &pk)

		resp, err := do(rr)
		if err != nil {
			if last != nil {
				return last, nil // an account already refused; that is the answer
			}
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusPaymentRequired:
			cool(k, scopePriced)
		case http.StatusUnauthorized:
			cool(k, scopeAll)
		case http.StatusTooManyRequests:
			until, spent := freeQuota(resp, keyNow())
			switch {
			case spent:
				coolUntil(k, scopeFree, until)
			case free:
				// Not this account's quota: the model is limited upstream, and every
				// account would be told the same. Asking them all multiplies one
				// request into a call per key; the pool moves it to another route.
				if last != nil {
					last.Body.Close()
				}
				return resp, nil
			}
		default:
			if last != nil {
				last.Body.Close()
			}
			return resp, nil
		}
		if last != nil {
			last.Body.Close()
		}
		last = resp
	}
	if last != nil {
		return last, nil
	}
	if free && quotaBenched(keys, keyNow()) {
		return poolCooling(r, keys), nil
	}
	return everyKeyCooling(r), nil
}

// freeQuota reads a 429 for the one refusal that is about the ACCOUNT's free
// allowance rather than about this request: the vendor's per-minute or per-day
// count of free-model requests. It answers when that count starts again.
//
// OpenRouter names the window in the message ("Rate limit exceeded:
// free-models-per-day") and the reset in X-RateLimit-Reset, milliseconds since
// the epoch, as a response header or inside error.metadata.headers. A reset it
// does not state is read as the window's own length: the next UTC midnight for a
// day, a minute for a minute.
//
// Any other 429 — a provider behind the vendor rate limiting one model — is not
// the account's, so it benches nothing and the next key is simply asked.
//
// The body is read and put back, so whoever answers with this response still
// has all of it.
func freeQuota(resp *http.Response, now time.Time) (time.Time, bool) {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(b))

	var e struct {
		Error struct {
			Message  string `json:"message"`
			Metadata struct {
				Headers map[string]string `json:"headers"`
			} `json:"metadata"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	// The vendor's own sentence, from its start: a message that merely CONTAINS
	// the words — an upstream quoting a prompt back — is not the account's quota.
	msg := strings.ToLower(strings.TrimSpace(e.Error.Message))
	var span time.Duration
	switch {
	case strings.HasPrefix(msg, "rate limit exceeded: free-models-per-day"):
		span = 24 * time.Hour
	case strings.HasPrefix(msg, "rate limit exceeded: free-models-per-min"):
		span = time.Minute
	default:
		return time.Time{}, false
	}
	reset := resp.Header.Get("X-RateLimit-Reset")
	for k, v := range e.Error.Metadata.Headers {
		if strings.EqualFold(k, "X-RateLimit-Reset") {
			reset = v
		}
	}
	if ms, err := strconv.ParseInt(strings.TrimSpace(reset), 10, 64); err == nil && ms > 0 {
		if at := time.UnixMilli(ms); at.After(now) && at.Sub(now) <= span {
			return at, true
		}
	}
	if span == time.Minute {
		return now.Add(time.Minute), true
	}
	return nextMidnight(now), true
}

// nextMidnight is the next 00:00 UTC after now.
func nextMidnight(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day()+1, 0, 0, 0, 0, time.UTC)
}

// everyKeyCooling is the answer when every key is sitting out: the 402 the
// vendor gave last, restated without a round trip, so the pipe treats it exactly
// as it treats the vendor's own.
func everyKeyCooling(r *http.Request) *http.Response {
	const body = `{"error":{"message":"every account with this vendor refused recently and is cooling","code":402}}`
	return restated(r, http.StatusPaymentRequired, body)
}

// poolCooling is the answer to a free request when every key is sitting out:
// the 429 the vendor gave, restated without a round trip. A 402 here would say
// our account cannot pay, which is false of a route nobody pays for.
func poolCooling(r *http.Request, keys []string) *http.Response {
	p := standing(keys, keyNow())
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"message": "every account in the free pool is at its free-model limit",
		"code":    http.StatusTooManyRequests,
	}})
	resp := restated(r, http.StatusTooManyRequests, string(body))
	if !p.Resets.IsZero() {
		resp.Header.Set("X-RateLimit-Reset", strconv.FormatInt(p.Resets.UnixMilli(), 10))
	}
	return resp
}

func restated(r *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        strconv.Itoa(status) + " " + http.StatusText(status),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}
}

// The free pool's three states.
const (
	PoolAvailable = "available" // every account takes free requests
	PoolBusy      = "busy"      // some account is out, or all are and one is back within a minute
	PoolExhausted = "exhausted" // no account takes free requests until Resets
)

// Pool is the free lane's standing across every vendor account it spends: how
// many accounts there are, how many take a free request now, and when the first
// one that does not comes back. It holds no key and no key id.
type Pool struct {
	State  string
	Keys   int
	Ready  int
	Resets time.Time // when the first account that is out comes back; zero when none is out
}

// FreePool is the free pool's standing now, as this process has heard it from
// the vendor.
//
// It is what the accounts' own answers said, and nothing else: an account is out
// only after the vendor refused it, and back when the vendor said it would be. So
// a process that has not yet sent a free request reports every account ready,
// which is true until the vendor says otherwise.
func FreePool() Pool {
	f := freeFamily()
	p := f.provider()
	if p == nil || strings.TrimSpace(p.ProviderUrl) == "" {
		return Pool{State: PoolExhausted}
	}
	return standing(f.ring(p), keyNow())
}

// standing folds what each key has said about free routes into the pool's state.
func standing(keys []string, now time.Time) Pool {
	p := Pool{Keys: len(keys)}
	for _, k := range keys {
		at := back(k, true, now)
		if at.IsZero() {
			p.Ready++
			continue
		}
		if p.Resets.IsZero() || at.Before(p.Resets) {
			p.Resets = at
		}
	}
	switch {
	case p.Keys == 0:
		p.State = PoolExhausted
	case p.Ready == p.Keys:
		p.State = PoolAvailable
	case p.Ready > 0 || p.Resets.Sub(now) <= time.Minute:
		p.State = PoolBusy
	default:
		p.State = PoolExhausted
	}
	return p
}
