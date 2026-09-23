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

// family_keys.go — a family served on several vendor accounts tries them in order.
//
// Every family request leaves through dispatch, and dispatch sends through
// modelFamily.send. A family that names more than one credential (keyNames) gets
// the ring below; every other family, and one whose admin row supplies its key,
// sends exactly as it did.
//
// An account-level refusal moves the SAME request to the next account:
//
//	402  the account cannot pay         → next key; this key cools for priced routes
//	401  the key is not accepted        → next key; this key cools for every route
//	429  the account is rate limited    → next key; nothing cools
//
// Anything else is the request's own answer and is returned as it came. When no
// key answers, the last refusal is returned unchanged, so the pipe maps it the
// way it maps any vendor refusal (a supply refusal, never the caller's debt).

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
)

// keyCool is how long a key that answered 402 or 401 sits out before it is
// asked again.
const keyCool = 3 * time.Minute

// keyScope is what a cooled key is out for. A 402 says the account cannot pay,
// which says nothing about a route the vendor serves for free; a 401 says the
// key itself is refused, which holds for every route.
type keyScope int

const (
	scopePriced keyScope = iota
	scopeAll
)

type keyCooling struct {
	id    string // keyID, never the key
	scope keyScope
}

var (
	keyCoolMu sync.Mutex
	keyCooled = map[keyCooling]time.Time{}
	keyNow    = time.Now // the clock the cooldown reads; tests move it
)

// keyID names a key in memory and in logs without holding the key.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// cooling reports whether key sits out a request for a route of this price.
func cooling(key string, free bool) bool {
	id := keyID(key)
	keyCoolMu.Lock()
	defer keyCoolMu.Unlock()
	now := keyNow()
	out := false
	for _, s := range []keyScope{scopeAll, scopePriced} {
		if s == scopePriced && free {
			continue
		}
		k := keyCooling{id, s}
		until, ok := keyCooled[k]
		if !ok {
			continue
		}
		if now.Before(until) {
			out = true
			continue
		}
		delete(keyCooled, k) // expired: probe it again
	}
	return out
}

func cool(key string, scope keyScope) {
	keyCoolMu.Lock()
	keyCooled[keyCooling{keyID(key), scope}] = keyNow().Add(keyCool)
	keyCoolMu.Unlock()
}

// forgetKeys clears every cooldown. Tests start from it.
func forgetKeys() {
	keyCoolMu.Lock()
	keyCooled = map[keyCooling]time.Time{}
	keyCoolMu.Unlock()
}

// keys returns the credentials this family's requests try, in order, or nil
// when it sends on the provider's one key.
func (f *modelFamily) keys() []string {
	if len(f.keyNames) == 0 {
		return nil
	}
	return object.FamilyKeys(f.name, f.keyNames)
}

// send issues r to the family's upstream. free says the route is one the vendor
// charges nothing for.
func (f *modelFamily) send(r *http.Request, p *object.Provider, free bool) (*http.Response, error) {
	keys := f.keys()
	if len(keys) == 0 {
		return zenPipeClient.Do(r)
	}
	return sendKeyed(r, p, keys, free, zenPipeClient.Do)
}

// sendKeyed issues r on each key in order until an upstream gives an answer
// that is not an account refusal. r must carry GetBody so each attempt sends
// the same body.
func sendKeyed(r *http.Request, p *object.Provider, keys []string, free bool, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	var last *http.Response
	for _, k := range keys {
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
	return everyKeyCooling(r), nil
}

// everyKeyCooling is the answer when every key is sitting out: the 402 the
// vendor gave last, restated without a round trip, so the pipe treats it exactly
// as it treats the vendor's own.
func everyKeyCooling(r *http.Request) *http.Response {
	const body = `{"error":{"message":"every account with this vendor refused recently and is cooling","code":402}}`
	return &http.Response{
		StatusCode:    http.StatusPaymentRequired,
		Status:        "402 Payment Required",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}
}
