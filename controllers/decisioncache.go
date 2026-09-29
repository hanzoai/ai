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
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/hanzoai/ai/object"
)

// An answer Kai gave in process is a function of the weights and the request, so an
// identical request from the same org within a minute is answered again from the
// answer held, and identical requests in flight together share one forward pass.
//
// THE ORG IS PART OF THE KEY. An answer held for one org is never given to another:
// a reply that came back faster because somebody else asked first would tell a
// caller what another tenant sent. Within an org the same caller, or a colleague,
// asked it; across orgs every request reaches the service.
//
// A held answer is billed exactly as a fresh one: it carries the usage the service
// reported, and the call is reserved, settled and debited on it the same way. It
// leaves under an id of its own. A request naming a handle is never held: the
// state behind it is the service's to keep.

// decisionTTL is how long an answer is given again for an identical request.
var decisionTTL = time.Minute

const (
	decisionMax   = 4096     // answers held
	decisionBytes = 64 << 20 // bytes of answers held
	// decisionShare is the part of the cache one org may hold, as a divisor: an org
	// asking more than its share evicts its own least recently used answers, never
	// another org's.
	decisionShare = 8
)

// held is one answer in the cache, for the org that asked it.
type held struct {
	id  [32]byte
	org string
	d   decided
}

// decisionCacheT is the cache's shape; decisionCache is the one.
type decisionCacheT struct {
	mu     sync.Mutex
	lru    *list.List                 // front is most recently used; values are *held
	m      map[[32]byte]*list.Element // by key
	size   int
	orgs   map[string]*share // what each org holds
	rev    map[string]string // upstream model → routing.sha256 of its latest 200 answer
	flight singleflight.Group
}

var decisionCache decisionCacheT

// share is what one org holds: answers and bytes.
type share struct{ n, size int }

// recall answers body, sent as up for org, from a live held answer, else asks the
// service once for every identical body in flight. An answer that was not produced
// by this caller's own call leaves under a fresh id.
func recall(ctx context.Context, kai *object.Provider, up, org, rid string, body []byte) decided {
	norm, ok := holdable(body)
	if !ok {
		return send(ctx, kai, rid, body)
	}
	c := &decisionCache
	c.mu.Lock()
	rev := c.rev[up]
	id := decisionKey(org, up, rev, norm)
	d, hit := c.lookup(id)
	c.mu.Unlock()
	if hit {
		d.body = remint(d.body)
		return d
	}
	if rev == "" {
		return learn(up, org, norm, send(ctx, kai, rid, body))
	}
	mine := false
	v, _, _ := c.flight.Do(string(id[:]), func() (any, error) {
		mine = true
		return learn(up, org, norm, send(context.WithoutCancel(ctx), kai, rid, body)), nil
	})
	d = v.(decided)
	if !mine {
		// The request id is the call's that made it; the wait it asks for is everyone's.
		header := make(map[string]string, len(d.header))
		for k, v := range d.header {
			if k != "X-Request-Id" {
				header[k] = v
			}
		}
		d.header = header
		if d.status == http.StatusOK {
			d.body = remint(d.body)
		}
	}
	return d
}

// learn takes the weights a 200 answer names as up's revision, and holds the
// answer for org when Kai gave it in process.
func learn(up, org string, norm []byte, d decided) decided {
	if d.fault != nil || d.status != http.StatusOK {
		return d
	}
	c := &decisionCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rev == nil {
		c.rev = make(map[string]string)
	}
	c.rev[up] = d.sha256
	if d.sha256 == "" || len(d.body) > decisionBytes {
		return d
	}
	c.hold(decisionKey(org, up, d.sha256, norm), org, d)
	return d
}

// lookup is the live answer held under id, marked most recently used; an expired
// one is dropped. Called with mu held.
func (c *decisionCacheT) lookup(id [32]byte) (decided, bool) {
	e, ok := c.m[id]
	if !ok {
		return decided{}, false
	}
	h := e.Value.(*held)
	if time.Now().After(h.d.exp) {
		c.drop(e)
		return decided{}, false
	}
	c.lru.MoveToFront(e)
	return h.d, true
}

// hold keeps d for org under id: past org's share, org's own least recently used
// answer goes first; past the whole cache's bound, the least recently used answer
// of any org. Called with mu held.
func (c *decisionCacheT) hold(id [32]byte, org string, d decided) {
	if len(d.body) > decisionBytes/decisionShare {
		return
	}
	if c.m == nil {
		c.lru, c.m, c.orgs, c.size = list.New(), map[[32]byte]*list.Element{}, map[string]*share{}, 0
	}
	if e, ok := c.m[id]; ok {
		c.drop(e)
	}
	mine := c.orgs[org]
	if mine == nil {
		mine = &share{}
		c.orgs[org] = mine
	}
	for over := true; over && mine.n > 0 && (mine.n >= decisionMax/decisionShare || mine.size+len(d.body) > decisionBytes/decisionShare); {
		over = false
		for e := c.lru.Back(); e != nil; e = e.Prev() {
			if e.Value.(*held).org == org {
				c.drop(e)
				over = true
				break
			}
		}
	}
	for c.lru.Len() > 0 && (c.lru.Len() >= decisionMax || c.size+len(d.body) > decisionBytes) {
		c.drop(c.lru.Back())
	}
	// Evicting the org's last answer let its share go; this answer brings it back.
	c.orgs[org] = mine
	d.header = nil
	d.exp = time.Now().Add(decisionTTL)
	c.m[id] = c.lru.PushFront(&held{id: id, org: org, d: d})
	c.size += len(d.body)
	mine.n++
	mine.size += len(d.body)
}

// drop removes one held answer. Called with mu held.
func (c *decisionCacheT) drop(e *list.Element) {
	h := c.lru.Remove(e).(*held)
	delete(c.m, h.id)
	c.size -= len(h.d.body)
	if s := c.orgs[h.org]; s != nil {
		s.n--
		s.size -= len(h.d.body)
		if s.n == 0 {
			delete(c.orgs, h.org)
		}
	}
}

// holdable is body without its insignificant whitespace, in the order it was
// written — question order is answer order — and false for a body naming state
// the service holds (observe, handle).
func holdable(body []byte) ([]byte, bool) {
	var head struct {
		Observe json.RawMessage `json:"observe"`
		Handle  json.RawMessage `json:"handle"`
	}
	if json.Unmarshal(body, &head) != nil || head.Observe != nil || head.Handle != nil {
		return nil, false
	}
	var norm bytes.Buffer
	if json.Compact(&norm, body) != nil {
		return nil, false
	}
	return norm.Bytes(), true
}

// decisionKey is SHA-256 of the org, the upstream model, its revision and the body,
// NUL between them.
func decisionKey(org, up, rev string, norm []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(org + "\x00" + up + "\x00" + rev + "\x00"))
	h.Write(norm)
	var id [32]byte
	h.Sum(id[:0])
	return id
}
