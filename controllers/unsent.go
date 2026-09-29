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
	"bufio"
	"sync"
)

// unsent is a stream's destination before the stream has one.
//
// A streamed answer's status line goes out when the handler hands zip its writer,
// and nothing can change it after. So the answer is produced into this first while
// the handler waits for the first byte: a request refused before it said anything
// can still be answered with a status, and one that has begun is handed to the
// stream with what it already said.
//
// Writes come from the goroutine producing the answer and attach from zip's stream
// callback, so every field is read under mu.
type unsent struct {
	mu   sync.Mutex
	buf  []byte
	to   *bufio.Writer
	once sync.Once
	// first is closed by the first non-empty write.
	first chan struct{}
}

func newUnsent() *unsent { return &unsent{first: make(chan struct{})} }

// Write keeps p until a destination is attached, and writes through to it after.
func (h *unsent) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.once.Do(func() { close(h.first) })
	if h.to == nil {
		h.buf = append(h.buf, p...)
		return len(p), nil
	}
	return h.to.Write(p)
}

// Flush pushes what has been written to the client, once there is one.
func (h *unsent) Flush() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.to == nil {
		return nil
	}
	return h.to.Flush()
}

// opened reports whether anything has been written.
func (h *unsent) opened() bool {
	select {
	case <-h.first:
		return true
	default:
		return false
	}
}

// attach makes w the destination. What was kept goes out first, flushed; every
// later write goes straight to w. An error is the client gone.
func (h *unsent) attach(w *bufio.Writer) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.to = w
	if len(h.buf) == 0 {
		return nil
	}
	kept := h.buf
	h.buf = nil
	if _, err := w.Write(kept); err != nil {
		return err
	}
	return w.Flush()
}
