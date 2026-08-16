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

package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// Sessions is the process session manager. The runtime sets it and binds it to
// the router; the logout path reaches it for SessionDestroy.
var Sessions *MemorySessions

// memoryStore is an in-memory session store keyed by a cookie session id.
type memoryStore struct {
	id       string
	mu       sync.RWMutex
	data     map[interface{}]interface{}
	accessed time.Time
}

func (s *memoryStore) Set(key, value interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *memoryStore) Get(key interface{}) interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[key]
}

func (s *memoryStore) Delete(key interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *memoryStore) SessionID() string { return s.id }

// SessionRelease is a no-op: an in-memory store needs no persistence step.
func (s *memoryStore) SessionRelease(http.ResponseWriter) {}

func (s *memoryStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = map[interface{}]interface{}{}
	return nil
}

// MemorySessions is a cookie-keyed, in-memory session manager. It backs the
// cookie web-admin flow (API requests authenticate per-request via bearer
// credentials, not sessions). Single-process by design, matching the
// single-replica runtime.
type MemorySessions struct {
	cookieName string
	maxAge     time.Duration
	secure     bool
	sameSite   http.SameSite

	mu     sync.Mutex
	stores map[string]*memoryStore
}

// NewMemorySessions builds a session manager for a cookie name and lifetime and
// starts its expiry sweep.
func NewMemorySessions(cookieName string, maxAge time.Duration) *MemorySessions {
	m := &MemorySessions{
		cookieName: cookieName,
		maxAge:     maxAge,
		sameSite:   http.SameSiteLaxMode,
		stores:     map[string]*memoryStore{},
	}
	go m.sweep()
	return m
}

// SessionStart returns the session named by the request cookie, or creates a
// new one and sets the cookie on the response.
func (m *MemorySessions) SessionStart(w http.ResponseWriter, r *http.Request) (Store, error) {
	if c, err := r.Cookie(m.cookieName); err == nil && c.Value != "" {
		m.mu.Lock()
		s, ok := m.stores[c.Value]
		m.mu.Unlock()
		if ok {
			s.touch()
			return s, nil
		}
	}
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	s := &memoryStore{id: id, data: map[interface{}]interface{}{}, accessed: time.Now()}
	m.mu.Lock()
	m.stores[id] = s
	m.mu.Unlock()
	m.writeCookie(w, id, int(m.maxAge/time.Second))
	return s, nil
}

// writeCookie is the ONE place this manager's cookie is described. Issuing,
// rotating and clearing all go through it, so a cookie can never be replaced by
// one carrying weaker attributes than the one it replaced — a rotation that
// dropped HttpOnly would hand back exactly what rotation exists to protect.
func (m *MemorySessions) writeCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: m.sameSite,
		MaxAge:   maxAge,
	})
}

// RegenerateID moves a session onto a NEW identifier and writes the new cookie.
//
// THE ID A CALLER HELD BEFORE THEY PROVED WHO THEY ARE MUST NEVER BE THE ID THEY
// HOLD AFTER. Anyone who can plant a value in the browser — one XSS on any host
// under this cookie's scope is enough, and an estate this size runs many — would
// otherwise be holding the very id that becomes authenticated the moment the
// victim signs in: a session nobody had to steal, because the attacker chose it.
//
// The old row is DELETED rather than left to lapse. Rotation that leaves the old
// id answering is not rotation; it is a second key to the same door.
//
// Values move across, because what rotates is the session's NAME and not its
// contents. Callers that write claims should do so AFTER rotating, so the
// credential only ever exists under the new id.
func (m *MemorySessions) RegenerateID(w http.ResponseWriter, old Store) (Store, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	s := &memoryStore{id: id, data: map[interface{}]interface{}{}, accessed: time.Now()}

	// A typed nil inside a non-nil interface is still nil to dereference, so the
	// assertion is what makes "no previous session" safe rather than a panic on a
	// path that runs during sign-in.
	var prev *memoryStore
	if old != nil {
		prev, _ = old.(*memoryStore)
	}
	if prev != nil {
		prev.mu.RLock()
		for k, v := range prev.data {
			s.data[k] = v
		}
		prev.mu.RUnlock()
	}

	m.mu.Lock()
	m.stores[id] = s
	if prev != nil {
		delete(m.stores, prev.id)
	}
	m.mu.Unlock()

	m.writeCookie(w, id, int(m.maxAge/time.Second))
	return s, nil
}

// ClearCookie expires this manager's cookie in the browser.
//
// Destroying the server-side session already makes the id answer nothing, so this
// is not what closes the door — it is what stops the browser carrying a dead
// handle for a year afterwards. Sign-out should do both: end the session, and
// stop telling the caller to keep presenting it.
func (m *MemorySessions) ClearCookie(w http.ResponseWriter) {
	m.writeCookie(w, "", -1)
}

// SessionDestroy removes a session by id (the logout path).
func (m *MemorySessions) SessionDestroy(id string) error {
	m.mu.Lock()
	delete(m.stores, id)
	m.mu.Unlock()
	return nil
}

func (s *memoryStore) touch() {
	s.mu.Lock()
	s.accessed = time.Now()
	s.mu.Unlock()
}

// sweep evicts sessions idle for longer than maxAge.
func (m *MemorySessions) sweep() {
	for range time.Tick(time.Minute) {
		cutoff := time.Now().Add(-m.maxAge)
		m.mu.Lock()
		for id, s := range m.stores {
			s.mu.RLock()
			idle := s.accessed.Before(cutoff)
			s.mu.RUnlock()
			if idle {
				delete(m.stores, id)
			}
		}
		m.mu.Unlock()
	}
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
