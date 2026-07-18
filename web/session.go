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
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: m.sameSite,
		MaxAge:   int(m.maxAge / time.Second),
	})
	return s, nil
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
