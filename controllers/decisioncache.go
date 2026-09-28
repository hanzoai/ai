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
)

var decisionCache struct {
	mu     sync.Mutex
	m      map[[32]byte]decided
	size   int
	rev    map[string]string // path and upstream model → routing.sha256 of its latest 200 answer
	flight singleflight.Group
}

// recall answers body, sent to path as up for org, from a live held answer, else
// asks the service once for every identical body in flight. An answer that was not
// produced by this caller's own call leaves under a fresh id.
func recall(ctx context.Context, kai *object.Provider, path, up, org, rid string, body []byte) decided {
	norm, ok := holdable(body)
	if !ok {
		return send(ctx, kai, path, rid, body)
	}
	c := &decisionCache
	line := path + "\x00" + up
	c.mu.Lock()
	rev := c.rev[line]
	id := decisionKey(org, line, rev, norm)
	d, held := c.m[id]
	c.mu.Unlock()
	if held && time.Now().Before(d.exp) {
		d.body = remint(d.body)
		return d
	}
	if rev == "" {
		return learn(line, org, norm, send(ctx, kai, path, rid, body))
	}
	mine := false
	v, _, _ := c.flight.Do(string(id[:]), func() (any, error) {
		mine = true
		return learn(line, org, norm, send(context.WithoutCancel(ctx), kai, path, rid, body)), nil
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

// learn takes the weights a 200 answer names as line's revision, and holds the
// answer for org when Kai gave it in process.
func learn(line, org string, norm []byte, d decided) decided {
	if d.fault != nil || d.status != http.StatusOK {
		return d
	}
	c := &decisionCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rev == nil {
		c.rev = make(map[string]string)
	}
	c.rev[line] = d.sha256
	if d.sha256 == "" || len(d.body) > decisionBytes {
		return d
	}
	id := decisionKey(org, line, d.sha256, norm)
	now := time.Now()
	if old, ok := c.m[id]; ok {
		delete(c.m, id)
		c.size -= len(old.body)
	}
	if len(c.m) >= decisionMax || c.size+len(d.body) > decisionBytes {
		for k, v := range c.m {
			if now.After(v.exp) {
				delete(c.m, k)
				c.size -= len(v.body)
			}
		}
	}
	if c.m == nil || len(c.m) >= decisionMax || c.size+len(d.body) > decisionBytes {
		c.m, c.size = make(map[[32]byte]decided), 0
	}
	held := d
	held.header = nil
	held.exp = now.Add(decisionTTL)
	c.m[id] = held
	c.size += len(d.body)
	return d
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

// decisionKey is SHA-256 of the org, the path and upstream model, its revision and
// the body, NUL between them.
func decisionKey(org, line, rev string, norm []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(org + "\x00" + line + "\x00" + rev + "\x00"))
	h.Write(norm)
	var id [32]byte
	h.Sum(id[:0])
	return id
}
