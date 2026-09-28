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
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// keyTTL is how long IAM's answer about a secret key is reused, so a revoked key
// stops resolving within it. Cloud's edge resolver (auth_apikey.go) holds a key as long.
var keyTTL = time.Minute

// keyMax bounds the answers held, so keys that never resolve cannot grow the map.
const keyMax = 1 << 16

type keyAnswer struct {
	user json.RawMessage // decoded afresh for each caller
	err  error
	exp  time.Time
}

var keys struct {
	mu     sync.Mutex
	m      map[[32]byte]keyAnswer
	flight singleflight.Group
}

// answerFor returns IAM's answer about key, asking only when no live answer is held.
// Callers of one key share one ask; an answer is held only when ask says it is final.
// Held by the SHA-256 of the IAM address and the key, never the key itself.
func answerFor(iamEndpoint, key string, ask func() (json.RawMessage, bool, error)) (json.RawMessage, error) {
	id := sha256.Sum256([]byte(iamEndpoint + "\x00" + key))
	keys.mu.Lock()
	a, ok := keys.m[id]
	keys.mu.Unlock()
	if ok && time.Now().Before(a.exp) {
		return a.user, a.err
	}
	v, _, _ := keys.flight.Do(string(id[:]), func() (any, error) {
		user, final, err := ask()
		a := keyAnswer{user: user, err: err, exp: time.Now().Add(keyTTL)}
		if final {
			hold(id, a)
		}
		return a, nil
	})
	a = v.(keyAnswer)
	return a.user, a.err
}

func hold(id [32]byte, a keyAnswer) {
	keys.mu.Lock()
	defer keys.mu.Unlock()
	if len(keys.m) >= keyMax {
		now := time.Now()
		for k, v := range keys.m {
			if now.After(v.exp) {
				delete(keys.m, k)
			}
		}
	}
	if keys.m == nil || len(keys.m) >= keyMax {
		keys.m = make(map[[32]byte]keyAnswer)
	}
	keys.m[id] = a
}
